# Demo audio — sources & licensing

| File | Source | License |
|------|--------|---------|
| `jfk.flac` | Excerpt of John F. Kennedy's 1961 inaugural address ("ask not what your country can do for you…"). Distributed as a test fixture in [openai/whisper](https://github.com/openai/whisper/blob/main/tests/jfk.flac). | Public domain (U.S. federal government work). |

Used by `scripts/seed_demo_data.sh` to exercise the real `AudioProcessor`
(faster-whisper transcription → text pipeline) via the `Q6-audio-jfk` search
assertion.
