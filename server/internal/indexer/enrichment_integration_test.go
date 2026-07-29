package indexer

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/workerpb"
)

func TestIndexerEnrichmentIntegration(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping DB integration test")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse MEM_TEST_DB: %v", err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatalf(
			"refusing to modify non-test database %q; MEM_TEST_DB must end in _test",
			config.ConnConfig.Database,
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	database, err := memdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	var userID uuid.UUID
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash)
		VALUES ($1, 'integration-test')
		RETURNING id
	`, "indexer-enrichment-"+uuid.NewString()+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Errorf("cleanup user: %v", err)
		}
	})

	explicitTime := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	fileID := uuid.New()
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO files (
			id, user_id, name, path, size, sha256, mime, storage_key,
			summary, tags, user_tags, timeline_at, geo, source_metadata,
			processor_metadata, index_status
		)
		VALUES (
			$1,$2,'photo.jpg','/',1,$3,'image/jpeg',$4,
			'legacy summary',ARRAY['manual'],ARRAY['manual'],$5,
			point(121.4737,31.2304),'{"source_kind":"mobile"}'::jsonb,
			'{}'::jsonb,'processing'
		)
	`,
		fileID,
		userID,
		strings.Repeat("a", 64),
		"indexer-test/"+fileID.String(),
		explicitTime,
	); err != nil {
		t.Fatalf("insert file: %v", err)
	}

	firstMetadata := []byte(`{
		"format":"JPEG",
		"timeline_at":"2020-01-02T03:04:05Z",
		"gps":{"lat":40.7128,"lng":-74.0060},
		"vlm_error":"raw provider body must not persist",
		"annotations":[
			{"kind":"description","value":"A city photo","confidence":0.81,"source":"model","provider":"fake:vlm","processor":"image","analysis_version":"file-enrichment-v1"},
			{"kind":"tag","value":"travel","confidence":0.91,"source":"model","provider":"fake:vlm","processor":"image","analysis_version":"file-enrichment-v1"},
			{"kind":"tag","value":"uncertain","confidence":0.41,"source":"model","provider":"fake:vlm","processor":"image","analysis_version":"file-enrichment-v1"}
		]
	}`)
	service := &Service{pool: database.Pool}
	if err := service.persist(ctx, fileID, &workerpb.ProcessResponse{
		Summary:      "must remain pending",
		Caption:      "raw AI observation",
		Tags:         []string{"must-not-replace"},
		MetadataJson: firstMetadata,
		Processor:    "image",
		Status:       workerpb.ProcessStatus_STATUS_PARTIAL,
	}); err != nil {
		t.Fatalf("persist first enrichment: %v", err)
	}

	assertIndexerFileProjection(
		t,
		ctx,
		database.Pool,
		fileID,
		explicitTime,
		121.4737,
		31.2304,
		[]string{"manual"},
		[]string{"manual"},
		"legacy summary",
		"partial",
	)
	var processorMetadata map[string]any
	if err := database.Pool.QueryRow(ctx, `
		SELECT processor_metadata FROM files WHERE id = $1
	`, fileID).Scan(&processorMetadata); err != nil {
		t.Fatalf("load processor metadata: %v", err)
	}
	degraded, degradedOK := processorMetadata["degraded_steps"].([]any)
	if _, leaked := processorMetadata["vlm_error"]; leaked ||
		!degradedOK ||
		!slices.Equal(degraded, []any{"vision_model"}) ||
		processorMetadata["caption_annotation_mismatch"] != true {
		t.Fatalf("processor metadata = %#v", processorMetadata)
	}
	var persistedCaption *string
	if err := database.Pool.QueryRow(ctx, `
		SELECT caption FROM files WHERE id = $1
	`, fileID).Scan(&persistedCaption); err != nil {
		t.Fatalf("load bounded caption: %v", err)
	}
	if persistedCaption == nil || *persistedCaption != "A city photo" {
		t.Fatalf("caption = %v, want structured description", persistedCaption)
	}

	var annotationCount int
	if err := database.Pool.QueryRow(ctx, `
		SELECT count(*) FROM file_annotations
		 WHERE file_id = $1 AND status = 'pending'
	`, fileID).Scan(&annotationCount); err != nil {
		t.Fatalf("count pending annotations: %v", err)
	}
	if annotationCount != 3 {
		t.Fatalf("pending annotations = %d, want 3", annotationCount)
	}

	if _, err := database.Pool.Exec(ctx, `
		UPDATE file_annotations
		   SET status = CASE value_text
				WHEN 'travel' THEN 'accepted'
				WHEN 'uncertain' THEN 'rejected'
				ELSE status
			END,
		       state_version = CASE
				WHEN value_text IN ('travel','uncertain') THEN 2
				ELSE state_version
			END,
		       decided_by_user_id = CASE
				WHEN value_text IN ('travel','uncertain') THEN $2
				ELSE decided_by_user_id
			END,
		       decided_at = CASE
				WHEN value_text IN ('travel','uncertain') THEN now()
				ELSE decided_at
			END
		 WHERE file_id = $1
	`, fileID, userID); err != nil {
		t.Fatalf("seed terminal review state: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		UPDATE files SET tags = ARRAY['manual','travel'] WHERE id = $1
	`, fileID); err != nil {
		t.Fatalf("seed accepted tag projection: %v", err)
	}

	secondMetadata := []byte(`{
		"annotations":[
			{"kind":"tag","value":"travel","confidence":0.99,"source":"model","provider":"fake:vlm-v2","processor":"image","analysis_version":"file-enrichment-v2"},
			{"kind":"tag","value":"uncertain","confidence":0.99,"source":"model","provider":"fake:vlm-v2","processor":"image","analysis_version":"file-enrichment-v2"},
			{"kind":"tag","value":"new","confidence":0.72,"source":"model","provider":"fake:vlm-v2","processor":"image","analysis_version":"file-enrichment-v2"}
		]
	}`)
	if err := service.persist(ctx, fileID, &workerpb.ProcessResponse{
		Summary:      "still pending",
		Tags:         []string{"still-must-not-replace"},
		MetadataJson: secondMetadata,
		Processor:    "image",
		Status:       workerpb.ProcessStatus_STATUS_OK,
	}); err != nil {
		t.Fatalf("persist repeated enrichment: %v", err)
	}
	assertIndexerFileProjection(
		t,
		ctx,
		database.Pool,
		fileID,
		explicitTime,
		121.4737,
		31.2304,
		[]string{"manual", "travel"},
		[]string{"manual"},
		"legacy summary",
		"done",
	)
	if err := database.Pool.QueryRow(ctx, `
		SELECT caption FROM files WHERE id = $1
	`, fileID).Scan(&persistedCaption); err != nil {
		t.Fatalf("load cleared caption: %v", err)
	}
	if persistedCaption != nil {
		t.Fatalf("caption = %q, want cleared without a current description", *persistedCaption)
	}

	rows, err := database.Pool.Query(ctx, `
		SELECT value_text, status, provider, analysis_version
		  FROM file_annotations
		 WHERE file_id = $1
		 ORDER BY value_text
	`, fileID)
	if err != nil {
		t.Fatalf("list annotations after reindex: %v", err)
	}
	defer rows.Close()
	statuses := make(map[string]string)
	providers := make(map[string]string)
	analysisVersions := make(map[string]string)
	rowCount := 0
	for rows.Next() {
		var value, status, provider, analysisVersion string
		if err := rows.Scan(&value, &status, &provider, &analysisVersion); err != nil {
			t.Fatalf("scan annotation: %v", err)
		}
		rowCount++
		statuses[value] = status
		providers[value] = provider
		analysisVersions[value] = analysisVersion
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate annotations: %v", err)
	}
	if rowCount != 4 ||
		len(statuses) != 4 ||
		statuses["travel"] != "accepted" ||
		statuses["uncertain"] != "rejected" ||
		statuses["A city photo"] != "superseded" ||
		statuses["new"] != "pending" ||
		providers["travel"] != "fake:vlm" ||
		providers["uncertain"] != "fake:vlm" ||
		analysisVersions["travel"] != "file-enrichment-v1" ||
		analysisVersions["uncertain"] != "file-enrichment-v1" ||
		analysisVersions["new"] != "file-enrichment-v2" {
		t.Fatalf(
			"annotation rows=%d states=%v providers=%v versions=%v",
			rowCount,
			statuses,
			providers,
			analysisVersions,
		)
	}
	rows.Close()

	updatedPendingMetadata := []byte(`{
		"annotations":[
			{"kind":"tag","value":"new","confidence":0.88,"source":"model","provider":"fake:vlm-v3","processor":"image","analysis_version":"file-enrichment-v2"}
		]
	}`)
	persistUpdatedPending := func() {
		t.Helper()
		if err := service.persist(ctx, fileID, &workerpb.ProcessResponse{
			MetadataJson: updatedPendingMetadata,
			Processor:    "image",
			Status:       workerpb.ProcessStatus_STATUS_OK,
		}); err != nil {
			t.Fatalf("persist changed pending suggestion: %v", err)
		}
	}
	persistUpdatedPending()
	var pendingVersion int64
	if err := database.Pool.QueryRow(ctx, `
		SELECT state_version
		  FROM file_annotations
		 WHERE file_id = $1 AND value_text = 'new'
	`, fileID).Scan(&pendingVersion); err != nil {
		t.Fatalf("load changed pending version: %v", err)
	}
	if pendingVersion != 2 {
		t.Fatalf("changed pending state_version = %d, want 2", pendingVersion)
	}
	persistUpdatedPending()
	if err := database.Pool.QueryRow(ctx, `
		SELECT state_version
		  FROM file_annotations
		 WHERE file_id = $1 AND value_text = 'new'
	`, fileID).Scan(&pendingVersion); err != nil {
		t.Fatalf("load replayed pending version: %v", err)
	}
	if pendingVersion != 2 {
		t.Fatalf("identical pending replay state_version = %d, want 2", pendingVersion)
	}

	if err := service.persist(ctx, fileID, &workerpb.ProcessResponse{
		MetadataJson: []byte(`{
			"annotations":[
				{"kind":"description","value":"A city photo","confidence":0.93,"source":"model","provider":"fake:vlm-v4","processor":"image","analysis_version":"file-enrichment-v2"}
			]
		}`),
		Processor: "image",
		Status:    workerpb.ProcessStatus_STATUS_OK,
	}); err != nil {
		t.Fatalf("revive current suggestion: %v", err)
	}
	var (
		revivedStatus  string
		revivedVersion int64
	)
	if err := database.Pool.QueryRow(ctx, `
		SELECT status, state_version
		  FROM file_annotations
		 WHERE file_id = $1 AND value_text = 'A city photo'
	`, fileID).Scan(&revivedStatus, &revivedVersion); err != nil {
		t.Fatalf("load revived suggestion: %v", err)
	}
	if revivedStatus != "pending" || revivedVersion != 3 {
		t.Fatalf(
			"revived suggestion status=%q version=%d, want pending/3",
			revivedStatus,
			revivedVersion,
		)
	}
	var supersededNewStatus string
	if err := database.Pool.QueryRow(ctx, `
		SELECT status
		  FROM file_annotations
		 WHERE file_id = $1 AND value_text = 'new'
	`, fileID).Scan(&supersededNewStatus); err != nil {
		t.Fatalf("load superseded replacement: %v", err)
	}
	if supersededNewStatus != "superseded" {
		t.Fatalf("replacement status = %q, want superseded", supersededNewStatus)
	}

	assertRevivedStatus := func(want string, wantVersion int64) {
		t.Helper()
		if err := database.Pool.QueryRow(ctx, `
			SELECT status, state_version
			  FROM file_annotations
			 WHERE file_id = $1 AND value_text = 'A city photo'
		`, fileID).Scan(&revivedStatus, &revivedVersion); err != nil {
			t.Fatalf("load empty-analysis result: %v", err)
		}
		if revivedStatus != want || revivedVersion != wantVersion {
			t.Fatalf(
				"empty-analysis result status=%q version=%d, want %s/%d",
				revivedStatus,
				revivedVersion,
				want,
				wantVersion,
			)
		}
	}
	incompleteSet := []byte(`{
		"annotations_complete":false,
		"annotations":[
			{"kind":"description","value":"A newer partial city photo","confidence":0.93,"source":"model","provider":"fake:vlm-v4","processor":"image","analysis_version":"file-enrichment-v2"},
			{"kind":"tag","value":"partial-only","confidence":0.61,"source":"model","provider":"fake:vlm-v4","processor":"image","analysis_version":"file-enrichment-v2"}
		]
	}`)
	if err := service.persist(ctx, fileID, &workerpb.ProcessResponse{
		MetadataJson: incompleteSet,
		Processor:    "image",
		Status:       workerpb.ProcessStatus_STATUS_OK,
	}); err != nil {
		t.Fatalf("persist incomplete suggestion set: %v", err)
	}
	assertRevivedStatus("pending", 3)
	var incompleteCaption *string
	if err := database.Pool.QueryRow(ctx, `
		SELECT caption FROM files WHERE id = $1
	`, fileID).Scan(&incompleteCaption); err != nil {
		t.Fatalf("load incomplete-set caption projection: %v", err)
	}
	if incompleteCaption == nil {
		t.Fatal("incomplete-set caption is nil, want newest pending description")
	}
	if *incompleteCaption != "A newer partial city photo" {
		t.Fatalf(
			"incomplete-set caption = %q, want newest pending description",
			*incompleteCaption,
		)
	}
	var (
		partialOnlyStatus  string
		partialOnlyVersion int64
	)
	if err := database.Pool.QueryRow(ctx, `
		SELECT status, state_version
		  FROM file_annotations
		 WHERE file_id = $1 AND value_text = 'partial-only'
	`, fileID).Scan(&partialOnlyStatus, &partialOnlyVersion); err != nil {
		t.Fatalf("load partial-only suggestion: %v", err)
	}
	if partialOnlyStatus != "pending" || partialOnlyVersion != 1 {
		t.Fatalf(
			"partial-only status=%q version=%d, want pending/1",
			partialOnlyStatus,
			partialOnlyVersion,
		)
	}
	incompleteSubset := []byte(`{
		"annotations_complete":false,
		"annotations":[
			{"kind":"description","value":"A city photo","confidence":0.93,"source":"model","provider":"fake:vlm-v4","processor":"image","analysis_version":"file-enrichment-v2"}
		]
	}`)
	if err := service.persist(ctx, fileID, &workerpb.ProcessResponse{
		MetadataJson: incompleteSubset,
		Processor:    "image",
		Status:       workerpb.ProcessStatus_STATUS_OK,
	}); err != nil {
		t.Fatalf("persist incomplete subset: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		SELECT status, state_version
		  FROM file_annotations
		 WHERE file_id = $1 AND value_text = 'partial-only'
	`, fileID).Scan(&partialOnlyStatus, &partialOnlyVersion); err != nil {
		t.Fatalf("reload partial-only suggestion: %v", err)
	}
	if partialOnlyStatus != "pending" || partialOnlyVersion != 1 {
		t.Fatalf(
			"incomplete subset superseded partial-only: status=%q version=%d",
			partialOnlyStatus,
			partialOnlyVersion,
		)
	}

	for _, metadata := range [][]byte{
		[]byte(`{"annotations":[]}`),
		[]byte(`{"annotations":[],"annotations_complete":false}`),
	} {
		if err := service.persist(ctx, fileID, &workerpb.ProcessResponse{
			MetadataJson: metadata,
			Processor:    "image",
			Status:       workerpb.ProcessStatus_STATUS_OK,
		}); err != nil {
			t.Fatalf("persist incomplete empty analysis: %v", err)
		}
		assertRevivedStatus("pending", 3)
	}
	if err := service.persist(ctx, fileID, &workerpb.ProcessResponse{
		MetadataJson: []byte(
			`{"annotations":[],"annotations_complete":true}`,
		),
		Processor: "image",
		Status:    workerpb.ProcessStatus_STATUS_OK,
	}); err != nil {
		t.Fatalf("persist completed empty analysis: %v", err)
	}
	assertRevivedStatus("superseded", 4)
	if err := database.Pool.QueryRow(ctx, `
		SELECT status, state_version
		  FROM file_annotations
		 WHERE file_id = $1 AND value_text = 'partial-only'
	`, fileID).Scan(&partialOnlyStatus, &partialOnlyVersion); err != nil {
		t.Fatalf("load completed-empty partial-only result: %v", err)
	}
	if partialOnlyStatus != "superseded" || partialOnlyVersion != 2 {
		t.Fatalf(
			"completed empty did not supersede partial-only: status=%q version=%d",
			partialOnlyStatus,
			partialOnlyVersion,
		)
	}

	rejectedFileID := uuid.New()
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO files (
			id, user_id, name, path, size, sha256, mime, storage_key,
			tags, user_tags, source_metadata, processor_metadata, index_status
		)
		VALUES (
			$1,$2,'rejected.jpg','/',1,$3,'image/jpeg',$4,
			'{}','{}','{}'::jsonb,'{}'::jsonb,'processing'
		)
	`,
		rejectedFileID,
		userID,
		strings.Repeat("c", 64),
		"indexer-test/"+rejectedFileID.String(),
	); err != nil {
		t.Fatalf("insert rejected-caption file: %v", err)
	}
	rejectedMetadata := []byte(`{
		"annotations_complete":true,
		"annotations":[
			{"kind":"description","value":"Rejected observation","confidence":0.77,"source":"model","provider":"fake:vlm","processor":"image","analysis_version":"file-enrichment-v1"}
		]
	}`)
	rejectedResponse := &workerpb.ProcessResponse{
		MetadataJson: rejectedMetadata,
		Processor:    "image",
		Status:       workerpb.ProcessStatus_STATUS_OK,
	}
	if err := service.persist(ctx, rejectedFileID, rejectedResponse); err != nil {
		t.Fatalf("persist reviewable caption: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		UPDATE file_annotations
		   SET status = 'rejected',
		       state_version = state_version + 1,
		       decided_by_user_id = $2,
		       decided_at = now(),
		       updated_at = now()
		 WHERE file_id = $1
		   AND kind = 'description'
	`, rejectedFileID, userID); err != nil {
		t.Fatalf("seed rejected description: %v", err)
	}
	if err := service.persist(ctx, rejectedFileID, rejectedResponse); err != nil {
		t.Fatalf("reindex rejected description: %v", err)
	}
	var (
		rejectedStatus  string
		rejectedCaption *string
	)
	if err := database.Pool.QueryRow(ctx, `
		SELECT annotation.status, files.caption
		  FROM file_annotations AS annotation
		  JOIN files ON files.id = annotation.file_id
		 WHERE annotation.file_id = $1
		   AND annotation.kind = 'description'
	`, rejectedFileID).Scan(&rejectedStatus, &rejectedCaption); err != nil {
		t.Fatalf("load rejected caption projection: %v", err)
	}
	if rejectedStatus != "rejected" || rejectedCaption != nil {
		t.Fatalf(
			"rejected reindex status=%q caption=%v, want rejected/null",
			rejectedStatus,
			rejectedCaption,
		)
	}

	retryFileID := uuid.New()
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO files (
			id, user_id, name, path, size, sha256, mime, storage_key,
			tags, user_tags, source_metadata, processor_metadata, index_status
		)
		VALUES (
			$1,$2,'retry.txt','/',1,$3,'text/plain',$4,
			'{}','{}','{}'::jsonb,'{}'::jsonb,'done'
		)
	`,
		retryFileID,
		userID,
		strings.Repeat("e", 64),
		"indexer-test/"+retryFileID.String(),
	); err != nil {
		t.Fatalf("insert retry file: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO embeddings_text (
			file_id, chunk_index, chunk_text, embedding, provider
		)
		VALUES ($1, 0, 'last usable text', $2::vector, 'fake:embed')
	`, retryFileID, vectorLiteral(make([]float32, 768))); err != nil {
		t.Fatalf("seed retry text embedding: %v", err)
	}
	if err := service.persist(ctx, retryFileID, &workerpb.ProcessResponse{
		MetadataJson: []byte(`{"embed_error":"provider unavailable"}`),
		Processor:    "text",
		Status:       workerpb.ProcessStatus_STATUS_PARTIAL,
	}); err != nil {
		t.Fatalf("persist partial retry without embedding: %v", err)
	}
	var retryEmbeddingCount int
	if err := database.Pool.QueryRow(ctx, `
		SELECT count(*) FROM embeddings_text WHERE file_id = $1
	`, retryFileID).Scan(&retryEmbeddingCount); err != nil {
		t.Fatalf("count preserved retry embedding: %v", err)
	}
	if retryEmbeddingCount != 1 {
		t.Fatalf(
			"partial retry embeddings = %d, want previous row preserved",
			retryEmbeddingCount,
		)
	}

	derivedFileID := uuid.New()
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO files (
			id, user_id, name, path, size, sha256, mime, storage_key,
			tags, user_tags, source_metadata, processor_metadata, index_status
		)
		VALUES (
			$1,$2,'derived.jpg','/',1,$3,'image/jpeg',$4,
			'{}','{}','{}'::jsonb,'{}'::jsonb,'processing'
		)
	`,
		derivedFileID,
		userID,
		strings.Repeat("b", 64),
		"indexer-test/"+derivedFileID.String(),
	); err != nil {
		t.Fatalf("insert derived-only file: %v", err)
	}
	if err := service.persist(ctx, derivedFileID, &workerpb.ProcessResponse{
		MetadataJson: []byte(`{
			"timeline_at":"2020-01-02T03:04:05Z",
			"gps":{"lat":40.7128,"lng":-74.006},
			"annotations":[]
		}`),
		Processor: "image",
		Status:    workerpb.ProcessStatus_STATUS_OK,
	}); err != nil {
		t.Fatalf("persist derived facts: %v", err)
	}
	assertIndexerFileProjection(
		t,
		ctx,
		database.Pool,
		derivedFileID,
		time.Date(2020, time.January, 2, 3, 4, 5, 0, time.UTC),
		-74.006,
		40.7128,
		[]string{},
		[]string{},
		"",
		"done",
	)
}

func assertIndexerFileProjection(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fileID uuid.UUID,
	wantTimeline time.Time,
	wantLon, wantLat float64,
	wantTags, wantUserTags []string,
	wantSummary, wantStatus string,
) {
	t.Helper()
	var (
		tags     []string
		userTags []string
		timeline time.Time
		geo      pgtype.Point
		summary  *string
		status   string
	)
	if err := pool.QueryRow(ctx, `
		SELECT tags, user_tags, timeline_at, geo, summary, index_status
		  FROM files
		 WHERE id = $1
	`, fileID).Scan(&tags, &userTags, &timeline, &geo, &summary, &status); err != nil {
		t.Fatalf("load file projection: %v", err)
	}
	actualSummary := ""
	if summary != nil {
		actualSummary = *summary
	}
	if !slices.Equal(tags, wantTags) ||
		!slices.Equal(userTags, wantUserTags) ||
		!timeline.Equal(wantTimeline) ||
		!geo.Valid || geo.P.X != wantLon || geo.P.Y != wantLat ||
		actualSummary != wantSummary ||
		status != wantStatus {
		encoded, _ := json.Marshal(map[string]any{
			"tags": tags, "user_tags": userTags, "timeline": timeline,
			"geo": geo, "summary": actualSummary, "status": status,
		})
		t.Fatalf("file projection = %s", encoded)
	}
}
