"""gRPC server entrypoint.

Run with::

    python -m mem_worker.server

Or, if the proto stubs are compiled inside an installed environment::

    mem-worker

The server:
  * binds to ``MEM_WORKER_GRPC_HOST:MEM_WORKER_GRPC_PORT`` (default ``127.0.0.1:50051``)
  * answers ``HealthCheck`` without touching Ollama/S3 (so liveness probes
    always succeed even in degraded mode)
  * dispatches ``Process`` to the registered processor for the file's mime
"""

from __future__ import annotations

import argparse
import json
import re
import signal
import sys
from concurrent import futures
from dataclasses import dataclass
from typing import Any, Optional

import grpc

from . import __version__
from .config import get_settings
from .logging import configure_logging, get_logger
from .processors import (
    AnnotationSuggestion,
    Entity,
    FileRef,
    ProcessorError,
    ProcessResult,
    default_registry,
)
from .storage import StorageError, fetch_bytes

log = get_logger(__name__)


# ---------------------------------------------------------------------------
# Per-request AI profile contract
# ---------------------------------------------------------------------------


AI_PROFILE_CONTRACT = "mem.ai-profile/v1"
_AI_PROFILE_FIELDS = {
    "contract",
    "id",
    "revision",
    "pipeline_revision",
    "embedding",
    "visual_embedding",
    "llm",
    "vlm",
    "asr",
    "rerank",
}
_PROFILE_IDENTIFIER = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")
_PROFILE_PROVIDER_SPEC = re.compile(
    r"[A-Za-z0-9][A-Za-z0-9_-]{0,31}:[A-Za-z0-9][A-Za-z0-9._:+/-]{0,222}"
)
# The current database schema is fixed at these dimensions. A profile with a
# different vector space belongs to the versioned-generation migration work,
# not a request-time override.
_TEXT_EMBEDDING_DIMENSION = 768
_VISUAL_EMBEDDING_DIMENSION = 512


class AIProfileError(ValueError):
    """Raised when an ``ai_profile`` option is not the exact v1 contract."""


@dataclass(frozen=True, slots=True)
class AIProfileStage:
    """One explicit capability setting from a server-resolved profile."""

    enabled: bool
    provider: str | None = None
    dimensions: int | None = None


@dataclass(frozen=True, slots=True)
class AIProfile:
    """Validated, immutable per-request AI routing contract.

    The Worker does not choose or mutate a profile. It only consumes the
    server-resolved, bounded profile sent in ``options_json``.
    """

    id: str
    revision: str
    pipeline_revision: str
    embedding: AIProfileStage
    visual_embedding: AIProfileStage
    llm: AIProfileStage
    vlm: AIProfileStage
    asr: AIProfileStage
    rerank: AIProfileStage

    def metadata(self) -> dict[str, str]:
        """Return the bounded provenance projection safe for Process metadata."""
        return {
            "contract": AI_PROFILE_CONTRACT,
            "id": self.id,
            "revision": self.revision,
            "pipeline_revision": self.pipeline_revision,
        }


def parse_ai_profile(options: dict[str, Any]) -> AIProfile | None:
    """Parse the optional, strict ``mem.ai-profile/v1`` request object.

    Existing callers without an ``ai_profile`` retain their legacy provider
    resolution. Once an object is present, it must be complete and valid;
    malformed data is never treated as permission to use ``MEM_DEFAULT_*``.
    """
    if "ai_profile" not in options:
        return None
    raw = options["ai_profile"]
    if not isinstance(raw, dict) or set(raw) != _AI_PROFILE_FIELDS:
        raise AIProfileError("invalid ai_profile")
    if raw.get("contract") != AI_PROFILE_CONTRACT:
        raise AIProfileError("invalid ai_profile")

    return AIProfile(
        id=_parse_profile_identifier(raw.get("id")),
        revision=_parse_profile_identifier(raw.get("revision")),
        pipeline_revision=_parse_profile_identifier(raw.get("pipeline_revision")),
        embedding=_parse_profile_stage(
            raw.get("embedding"),
            dimensions=True,
            expected_dimension=_TEXT_EMBEDDING_DIMENSION,
        ),
        visual_embedding=_parse_profile_stage(
            raw.get("visual_embedding"),
            dimensions=True,
            expected_dimension=_VISUAL_EMBEDDING_DIMENSION,
        ),
        llm=_parse_profile_stage(raw.get("llm")),
        vlm=_parse_profile_stage(raw.get("vlm")),
        asr=_parse_profile_stage(raw.get("asr")),
        rerank=_parse_profile_stage(raw.get("rerank")),
    )


def _parse_profile_identifier(value: Any) -> str:
    if not isinstance(value, str) or not _PROFILE_IDENTIFIER.fullmatch(value):
        raise AIProfileError("invalid ai_profile")
    return value


def _parse_profile_stage(
    raw: Any,
    *,
    dimensions: bool = False,
    expected_dimension: int | None = None,
) -> AIProfileStage:
    if not isinstance(raw, dict) or type(raw.get("enabled")) is not bool:
        raise AIProfileError("invalid ai_profile")

    enabled = raw["enabled"]
    if not enabled:
        if set(raw) != {"enabled"}:
            raise AIProfileError("invalid ai_profile")
        return AIProfileStage(enabled=False)

    expected_fields = {"enabled", "provider"}
    if dimensions:
        expected_fields.add("dimensions")
    if set(raw) != expected_fields:
        raise AIProfileError("invalid ai_profile")
    provider = _parse_profile_provider(raw.get("provider"))
    if not dimensions:
        return AIProfileStage(enabled=True, provider=provider)

    value = raw.get("dimensions")
    if (
        isinstance(value, bool)
        or not isinstance(value, int)
        or value <= 0
        or value != expected_dimension
    ):
        raise AIProfileError("invalid ai_profile")
    return AIProfileStage(enabled=True, provider=provider, dimensions=value)


def _parse_profile_provider(value: Any) -> str:
    if (
        not isinstance(value, str)
        or not _PROFILE_PROVIDER_SPEC.fullmatch(value)
        or "://" in value
    ):
        raise AIProfileError("invalid ai_profile")
    return value


def _profile_metadata_json(profile: AIProfile | None) -> bytes:
    if profile is None:
        return b""
    return json.dumps({"ai_profile": profile.metadata()}).encode("utf-8")


def _load_pb():
    """Import generated proto modules.

    We import lazily and with a helpful error so that ``python -m mem_worker.server``
    tells the developer exactly which Make target to run.
    """
    try:
        from .proto import processor_pb2 as pb
        from .proto import processor_pb2_grpc as pbg
    except ImportError as exc:
        raise SystemExit(
            "mem-worker: gRPC stubs are missing. Run `make proto` to generate them "
            f"(import error: {exc})."
        ) from exc
    return pb, pbg


# ---------------------------------------------------------------------------
# gRPC servicer
# ---------------------------------------------------------------------------


class ProcessorServicer:
    """Implements ProcessorService defined in proto/processor.proto."""

    def __init__(self, pb, pbg):
        self._pb = pb
        self._pbg = pbg
        self._registry = default_registry()
        log.info(
            "servicer.ready",
            processors=[p.name for p in self._registry.all()],
        )

    # ---- Chat ----

    def Chat(self, request, context):
        """Retired RPC: answer generation belongs to the external Agent."""
        context.abort(
            grpc.StatusCode.UNIMPLEMENTED,
            "Chat RPC retired; use mem_context through memd/MCP",
        )

    # ---- HealthCheck ----

    def HealthCheck(self, request, context):
        pb = self._pb
        return pb.HealthCheckResponse(
            status=pb.HealthCheckResponse.SERVING,
            version=__version__,
        )

    # ---- helpers ----

    def _pick_processor(
        self,
        mime: str,
        options: dict,
        *,
        ai_profile: AIProfile | None = None,
    ):
        """Resolve a Processor for ``mime`` with legacy or profiled routing.

        A validated profile wins over every legacy ``*_provider`` option. It
        supplies an explicit setting for every capability, so profiled work
        cannot fall through to process-wide ``MEM_DEFAULT_*`` values.

        Legacy override keys (all optional, ``"<vendor>:<model>"`` form):
            embedding_provider        — TEXT embedder (for TextProcessor only)
            visual_embedding_provider — VISUAL embedder (for ImageProcessor only)
            llm_provider              — text/PDF/audio annotation model
            vlm_provider              — image captioner

        Note: the text and visual embedder kinds are intentionally separate.
        A user who has ``embedding=ollama:mxbai-embed-large`` (1024-d) MUST
        still index images with CLIP (512-d) to keep ``embeddings_visual``
        column type consistent — these two latent spaces never share storage.
        """
        base = self._registry.find(mime)
        if base is None:
            return None
        if ai_profile is None and "ai_profile" in options:
            ai_profile = parse_ai_profile(options)
        if ai_profile is not None:
            return self._pick_profiled_processor(base, ai_profile)

        emb_spec = options.get("embedding_provider") or ""
        v_emb_spec = options.get("visual_embedding_provider") or ""
        llm_spec = options.get("llm_provider") or ""
        vlm_spec = options.get("vlm_provider") or ""
        if not (emb_spec or v_emb_spec or llm_spec or vlm_spec):
            return base
        # Lazy import to avoid cycle when default_registry() pulls in this file.
        from .processors.audio import AudioProcessor
        from .processors.image import ImageProcessor
        from .processors.pdf import PDFProcessor
        from .processors.text import TextProcessor
        from .providers import get_embedding_provider, get_vlm_provider

        vlm = get_vlm_provider(vlm_spec) if vlm_spec else None
        emb = get_embedding_provider(emb_spec) if emb_spec else None

        if isinstance(base, TextProcessor):
            return TextProcessor(embedder=emb, llm_spec=llm_spec or None)
        if isinstance(base, PDFProcessor):
            return PDFProcessor(embedder=emb, llm_spec=llm_spec or None)
        if isinstance(base, AudioProcessor):
            return AudioProcessor(embedder=emb, llm_spec=llm_spec or None)
        if isinstance(base, ImageProcessor):
            v_emb = get_embedding_provider(v_emb_spec) if v_emb_spec else None
            return ImageProcessor(embedder=v_emb, vlm=vlm)
        return base

    @staticmethod
    def _pick_profiled_processor(base, profile: AIProfile):
        """Build a per-request processor that never resolves default models."""
        # Lazy imports avoid a cycle while the process-wide default registry is
        # constructed. Providers themselves remain lazy inside processors, so
        # an invalid/unavailable managed model becomes a bounded partial stage.
        from .processors.audio import AudioProcessor
        from .processors.image import ImageProcessor
        from .processors.pdf import PDFProcessor
        from .processors.text import TextProcessor

        text_kwargs = {
            "embedding_enabled": profile.embedding.enabled,
            "embedding_spec": profile.embedding.provider,
            "embedding_dimensions": profile.embedding.dimensions,
            "llm_enabled": profile.llm.enabled,
            "llm_spec": profile.llm.provider,
            # Annotation rows carry the pipeline revision; the enclosing
            # Process metadata carries the full profile id/revision tuple.
            "analysis_version": profile.pipeline_revision,
        }
        if isinstance(base, TextProcessor):
            return TextProcessor(**text_kwargs)
        if isinstance(base, PDFProcessor):
            return PDFProcessor(**text_kwargs)
        if isinstance(base, AudioProcessor):
            return AudioProcessor(
                asr_enabled=profile.asr.enabled,
                asr_spec=profile.asr.provider,
                **text_kwargs,
            )
        if isinstance(base, ImageProcessor):
            return ImageProcessor(
                vlm_enabled=profile.vlm.enabled,
                vlm_spec=profile.vlm.provider,
                visual_embedding_enabled=profile.visual_embedding.enabled,
                visual_embedding_spec=profile.visual_embedding.provider,
                visual_embedding_dimensions=profile.visual_embedding.dimensions,
                analysis_version=profile.pipeline_revision,
            )
        return base

    # ---- Process ----

    def Process(self, request, context):
        pb = self._pb
        log.info(
            "process.start",
            file_id=request.file_id,
            mime=request.mime,
            storage_uri=request.storage_uri,
        )

        options: dict[str, Any] = {}
        if request.options_json:
            try:
                decoded_options = json.loads(request.options_json.decode("utf-8"))
                if not isinstance(decoded_options, dict):
                    raise ValueError("options_json must decode to an object")
                options = decoded_options
            except (UnicodeDecodeError, json.JSONDecodeError) as exc:
                log.warning("process.bad_options_json", error=str(exc))
                # A caller with malformed routing options must not be silently
                # recovered by falling back to a process-wide default model.
                # In particular, a truncated profile contract must never turn
                # into an unmetered managed/default provider invocation.
                return pb.ProcessResponse(
                    status=pb.STATUS_FAILED,
                    error="invalid options_json",
                )
            except ValueError as exc:
                log.warning("process.bad_options_json", error=str(exc))
                return pb.ProcessResponse(
                    status=pb.STATUS_FAILED,
                    error="invalid options_json",
                )

        try:
            ai_profile = parse_ai_profile(options)
        except AIProfileError:
            # Never expose caller-provided profile content or use a global
            # default as a recovery path for an invalid profile.
            log.warning("process.invalid_ai_profile", file_id=request.file_id)
            return pb.ProcessResponse(
                status=pb.STATUS_FAILED,
                error="invalid ai_profile",
            )

        # 1. Pick processor. If the caller supplied provider overrides via
        # options_json, build a per-request processor instance so we don't
        # mutate the singleton in the registry (concurrency-safe).
        proc = self._pick_processor(request.mime, options, ai_profile=ai_profile)
        if proc is None:
            log.info("process.skipped", file_id=request.file_id, mime=request.mime)
            return pb.ProcessResponse(
                status=pb.STATUS_SKIPPED,
                processor="",
                error=f"no processor for mime {request.mime!r}",
                metadata_json=_profile_metadata_json(ai_profile),
            )

        # 2. Fetch bytes
        try:
            data = fetch_bytes(request.storage_uri)
        except StorageError as exc:
            log.error("process.fetch_failed", file_id=request.file_id, error=str(exc))
            return pb.ProcessResponse(
                status=pb.STATUS_FAILED,
                processor=proc.name,
                error=f"storage: {exc}",
                metadata_json=_profile_metadata_json(ai_profile),
            )

        # 3. Dispatch
        file_ref = FileRef(
            file_id=request.file_id,
            storage_uri=request.storage_uri,
            mime=request.mime,
            sha256=request.sha256,
            user_id=request.user_id,
            name=request.name,
            data=data,
            options=options,
        )
        try:
            result = proc.process(file_ref)
        except ProcessorError as exc:
            log.error("process.failed", file_id=request.file_id, error=str(exc))
            return pb.ProcessResponse(
                status=pb.STATUS_FAILED,
                processor=proc.name,
                error=str(exc),
                metadata_json=_profile_metadata_json(ai_profile),
            )
        except Exception as exc:  # noqa: BLE001 — never let one bad file kill the server
            log.exception("process.unexpected", file_id=request.file_id)
            return pb.ProcessResponse(
                status=pb.STATUS_FAILED,
                processor=proc.name,
                error=f"unexpected: {exc}",
                metadata_json=_profile_metadata_json(ai_profile),
            )

        if ai_profile is not None:
            # Override rather than merge any same-named processor metadata so
            # downstream consumers see only the server-resolved provenance.
            result.metadata["ai_profile"] = ai_profile.metadata()

        resp = _result_to_proto(result, pb)
        log.info(
            "process.ok",
            file_id=request.file_id,
            processor=proc.name,
            embeddings=list(result.embeddings.keys()),
            tags=len(result.tags),
            entities=len(result.entities),
        )
        return resp


# ---------------------------------------------------------------------------
# ProcessResult -> protobuf
# ---------------------------------------------------------------------------


def _result_to_proto(result: ProcessResult, pb: Any):
    """Convert a Python :class:`ProcessResult` into the protobuf message."""
    # Processors degrade individual stages by recording a bounded ``*_error``
    # marker and returning the useful remainder. Keep the top-level status in
    # sync for every current and future stage instead of maintaining a list
    # that can silently miss PDF/ASR/face failures.
    has_error = any(k == "error" or k.endswith("_error") for k in result.metadata)
    status = pb.STATUS_PARTIAL if has_error else pb.STATUS_OK

    metadata = dict(result.metadata)
    metadata["annotations"] = _annotations_to_metadata(result)
    metadata["annotations_complete"] = result.annotations_complete

    pb_resp = pb.ProcessResponse(
        summary=result.summary or "",
        caption=result.caption or "",
        tags=list(result.tags),
        processor=result.processor,
        status=status,
        metadata_json=json.dumps(metadata, default=str).encode("utf-8"),
    )

    for ent in result.entities:
        pb_resp.entities.append(_entity_to_proto(ent, pb))

    for kind, emb_set in result.embeddings.items():
        pb_emb = pb.Embedding(provider=emb_set.provider, dim=emb_set.dim)
        for row in emb_set.rows:
            pb_emb.rows.append(
                pb.EmbeddingRow(
                    values=row.values,
                    index=row.index,
                    chunk_text=row.chunk_text,
                    metadata_json=json.dumps(row.metadata, default=str).encode("utf-8"),
                )
            )
        pb_resp.embeddings[kind].CopyFrom(pb_emb)

    return pb_resp


def _annotations_to_metadata(result: ProcessResult) -> list[dict[str, Any]]:
    """Serialize a final bounded annotation projection for the indexer."""

    output: list[dict[str, Any]] = []
    description_seen = False
    tags_seen: set[str] = set()

    for suggestion in result.annotations:
        if not isinstance(suggestion, AnnotationSuggestion):
            continue
        if suggestion.kind == "description":
            if description_seen:
                continue
            description_seen = True
        else:
            key = suggestion.value.casefold()
            if key in tags_seen or len(tags_seen) >= 20:
                continue
            tags_seen.add(key)

        output.append(
            {
                "kind": suggestion.kind,
                "value": suggestion.value,
                "confidence": suggestion.confidence,
                "source": suggestion.source,
                "provider": suggestion.provider,
                "processor": suggestion.processor or result.processor,
                "analysis_version": suggestion.analysis_version,
            }
        )

    return output


def _entity_to_proto(ent: Entity, pb: Any):
    return pb.Entity(
        type=ent.type,
        name=ent.name,
        confidence=ent.confidence,
        metadata_json=json.dumps(ent.metadata, default=str).encode("utf-8"),
    )


# ---------------------------------------------------------------------------
# Server lifecycle
# ---------------------------------------------------------------------------


def serve(host: Optional[str] = None, port: Optional[int] = None) -> None:
    """Bind a grpc.Server and block forever."""
    configure_logging()
    settings = get_settings()
    host = host or settings.grpc_host
    port = port or settings.grpc_port

    pb, pbg = _load_pb()

    server = grpc.server(
        futures.ThreadPoolExecutor(max_workers=settings.grpc_max_workers),
        options=[
            ("grpc.max_send_message_length", 64 * 1024 * 1024),
            ("grpc.max_receive_message_length", 64 * 1024 * 1024),
        ],
    )
    servicer = ProcessorServicer(pb, pbg)
    pbg.add_ProcessorServiceServicer_to_server(servicer, server)

    bind_addr = f"{host}:{port}"
    server.add_insecure_port(bind_addr)
    server.start()
    log.info(
        "server.listening",
        addr=bind_addr,
        version=__version__,
        max_workers=settings.grpc_max_workers,
    )

    # Graceful shutdown on SIGINT / SIGTERM. signal.signal() is only legal
    # on the main thread; skip silently otherwise (e.g. when the server is
    # spawned in a test harness thread).
    def _shutdown(signum, _frame):
        log.info("server.shutdown", signal=signum)
        server.stop(grace=5).wait()

    try:
        signal.signal(signal.SIGINT, _shutdown)
        signal.signal(signal.SIGTERM, _shutdown)
    except ValueError:
        log.debug("server.signal_skipped", reason="not on main thread")

    server.wait_for_termination()


def main(argv: Optional[list[str]] = None) -> int:
    parser = argparse.ArgumentParser(prog="mem-worker", description=__doc__)
    parser.add_argument("--host", default=None, help="gRPC bind host")
    parser.add_argument("--port", type=int, default=None, help="gRPC bind port")
    args = parser.parse_args(argv)
    try:
        serve(host=args.host, port=args.port)
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
