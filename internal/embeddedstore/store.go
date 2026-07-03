package embeddedstore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	_ "modernc.org/sqlite"
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
)

// sqlDB is the storage layer. Each collection lives in its own SQLite
// table; the metadata table records per-collection state (vector dimension).
type sqlDB struct {
	db *sql.DB
	// dimension hint per collection, populated lazily on first write.
	dimMu sync.Mutex
	dims  map[string]uint64
}

// openDB opens (or creates) the SQLite file at path. If path is empty,
// ":memory:" is used.
func openDB(path string) (*sqlDB, error) {
	if path == "" {
		path = ":memory:"
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &sqlDB{db: db, dims: make(map[string]uint64)}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the metadata table and renames any collection tables
// created under the legacy (non-injective) tableName scheme to their
// current names, using the collection registry as the source of truth for
// the original collection names. Registered collections whose legacy and
// current names differ get an ALTER TABLE RENAME; databases created after
// the injective scheme are untouched.
func (s *sqlDB) migrate() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS embedded_collections (
			name TEXT PRIMARY KEY,
			vector_size INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		);
	`); err != nil {
		return err
	}
	rows, err := s.db.Query(`SELECT name FROM embedded_collections`)
	if err != nil {
		return err
	}
	var collections []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		collections = append(collections, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range collections {
		oldTbl, newTbl := legacyTableName(name), tableName(name)
		if oldTbl == newTbl {
			continue
		}
		var haveOld, haveNew int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, oldTbl,
		).Scan(&haveOld); err != nil {
			return err
		}
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, newTbl,
		).Scan(&haveNew); err != nil {
			return err
		}
		if haveOld == 1 && haveNew == 0 {
			if _, err := s.db.Exec(
				fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, oldTbl, newTbl),
			); err != nil {
				return fmt.Errorf("migrate collection %q table: %w", name, err)
			}
			// The source_file index keeps its old name after the rename;
			// ensureCollection's CREATE INDEX IF NOT EXISTS will add the
			// new-name index on next write, which is harmless.
		}
	}
	return nil
}

// Close releases the SQLite handle.
func (s *sqlDB) Close() error { return s.db.Close() }

// tableName returns the SQL table name for a collection. The mapping MUST
// be injective: two distinct collection names must never share a table, or
// vault isolation silently breaks (the earlier scheme mapped every
// non-alphanumeric byte to "_", so `soul-x`, `soul_x`, and `soul:x` all
// merged into one table — cross-vault contamination). Lowercase letters
// and digits pass through; every other byte (including '_' and uppercase,
// because SQLite identifiers are case-insensitive) becomes "_" + two hex
// digits, so escapes cannot be forged. A leading "_" keeps the result a
// valid SQL identifier. migrate() renames tables created under the old
// scheme using the collection registry, so existing databases keep their
// data.
func tableName(collection string) string {
	safe := make([]byte, 0, len(collection)*3+1)
	safe = append(safe, '_')
	for i := 0; i < len(collection); i++ {
		c := collection[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			safe = append(safe, c)
		} else {
			safe = append(safe, '_', hexDigit(c>>4), hexDigit(c&0xf))
		}
	}
	return string(safe)
}

func hexDigit(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + b - 10
}

// legacyTableName is the pre-injective scheme, kept only so migrate() can
// find and rename tables created by it.
func legacyTableName(collection string) string {
	safe := make([]byte, 0, len(collection)+1)
	safe = append(safe, '_')
	for i := 0; i < len(collection); i++ {
		c := collection[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			safe = append(safe, c)
		} else {
			safe = append(safe, '_')
		}
	}
	return string(safe)
}

// ensureCollection creates the table for the collection if it does not
// exist and records the vector size.
func (s *Store) ensureCollection(ctx context.Context, collection string, vectorSize uint64) error {
	if collection == "" {
		collection = s.collection
	}
	tbl := tableName(collection)
	stmt := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id_key TEXT PRIMARY KEY,
			id_kind TEXT NOT NULL,
			vector BLOB,
			payload TEXT NOT NULL DEFAULT '{}',
			source_file TEXT
		);
		CREATE INDEX IF NOT EXISTS %s_source_idx ON %s(source_file);
	`, tbl, tbl, tbl)
	if _, err := s.conn.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create collection %q: %w", collection, err)
	}
	_, err := s.conn.db.ExecContext(ctx, `
		INSERT INTO embedded_collections(name, vector_size, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET vector_size = MAX(vector_size, excluded.vector_size)
	`, collection, int64(vectorSize), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("register collection %q: %w", collection, err)
	}
	s.conn.dimMu.Lock()
	s.conn.dims[collection] = vectorSize
	s.conn.dimMu.Unlock()
	return nil
}

// persistDimension records the largest observed vector size for a collection.
func (s *Store) persistDimension(ctx context.Context, collection string) error {
	s.conn.dimMu.Lock()
	d := s.conn.dims[collection]
	s.conn.dimMu.Unlock()
	_, err := s.conn.db.ExecContext(ctx, `
		UPDATE embedded_collections SET vector_size = ? WHERE name = ? AND vector_size < ?
	`, int64(d), collection, int64(d))
	return err
}

func (s *sqlDB) getVectorSize(ctx context.Context, collection string) (uint64, error) {
	s.dimMu.Lock()
	if d, ok := s.dims[collection]; ok {
		s.dimMu.Unlock()
		return d, nil
	}
	s.dimMu.Unlock()
	var size int64
	err := s.db.QueryRowContext(ctx, `SELECT vector_size FROM embedded_collections WHERE name = ?`, collection).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	s.dimMu.Lock()
	s.dims[collection] = uint64(size)
	s.dimMu.Unlock()
	return uint64(size), nil
}

// ── Upsert ───────────────────────────────────────────────────────────────────

// Upsert writes one or more points to the primary collection.
func (s *Store) Upsert(ctx context.Context, points []*pb.PointStruct) error {
	return s.upsertInto(ctx, s.collection, points)
}

// UpsertInto writes points to an explicit collection.
func (s *Store) UpsertInto(ctx context.Context, collection string, points []*pb.PointStruct) error {
	return s.upsertInto(ctx, collection, points)
}

func (s *Store) upsertInto(ctx context.Context, collection string, points []*pb.PointStruct) error {
	if len(points) == 0 {
		return nil
	}
	if err := s.ensureCollection(ctx, collection, 0); err != nil {
		return err
	}
	tbl := tableName(collection)
	stmt, err := s.conn.db.PrepareContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id_key, id_kind, vector, payload, source_file)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id_key) DO UPDATE SET
			id_kind = excluded.id_kind,
			vector = excluded.vector,
			payload = excluded.payload,
			source_file = excluded.source_file
	`, tbl))
	if err != nil {
		return err
	}
	defer stmt.Close()

	tx, err := s.conn.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, p := range points {
		id, kind, err := encodePointID(p.Id)
		if err != nil {
			return fmt.Errorf("upsert %q: %w", collection, err)
		}
		vec, err := encodeVector(p.GetVectors())
		if err != nil {
			return fmt.Errorf("upsert %q: %w", collection, err)
		}
		if len(vec) > 0 {
			s.conn.dimMu.Lock()
			if cur, ok := s.conn.dims[collection]; !ok || cur < uint64(len(vec)) {
				s.conn.dims[collection] = uint64(len(vec))
			}
			s.conn.dimMu.Unlock()
		}
		payload, err := encodePayload(p.GetPayload())
		if err != nil {
			return fmt.Errorf("upsert %q: %w", collection, err)
		}
		var src sql.NullString
		if v, ok := p.GetPayload()["source_file"]; ok {
			if sv, ok := v.GetKind().(*pb.Value_StringValue); ok {
				src = sql.NullString{String: sv.StringValue, Valid: true}
			}
		}
		if _, err := tx.StmtContext(ctx, stmt).ExecContext(ctx, id, kind, vec, payload, src); err != nil {
			return fmt.Errorf("upsert %q: %w", collection, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.persistDimension(ctx, collection); err != nil {
		return err
	}
	return nil
}

// ── Search ───────────────────────────────────────────────────────────────────

// Search performs a cosine-similarity search over the primary collection.
func (s *Store) Search(ctx context.Context, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string, filter *pb.Filter) ([]*pb.ScoredPoint, error) {
	return s.searchCollection(ctx, s.collection, vector, limit, scoreThreshold, sourceFilter, filter)
}

func (s *Store) searchCollection(ctx context.Context, collection string, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string, filter *pb.Filter) ([]*pb.ScoredPoint, error) {
	if len(vector) == 0 {
		return nil, nil
	}
	dim := uint64(len(vector))
	tbl := tableName(collection)
	rows, err := s.conn.db.QueryContext(ctx, fmt.Sprintf(`SELECT id_key, id_kind, vector, payload FROM %s`, tbl))
	if err != nil {
		return nil, fmt.Errorf("search %q: %w", collection, err)
	}
	defer rows.Close()
	scored := make([]*candidate, 0, 64)
	for rows.Next() {
		var idKey, idKind string
		var vecBlob []byte
		var payloadJSON string
		if err := rows.Scan(&idKey, &idKind, &vecBlob, &payloadJSON); err != nil {
			return nil, err
		}
		if len(vecBlob) == 0 {
			continue
		}
		if uint64(len(vecBlob)/4) != dim {
			continue
		}
		ptVec := decodeVector(vecBlob)
		if ptVec == nil {
			continue
		}
		score := cosineSimilarity(vector, ptVec)
		if score < scoreThreshold {
			continue
		}
		payload := decodePayload(payloadJSON)
		if sourceFilter != "" {
			if stringPayload(payload, "source_file") != sourceFilter {
				continue
			}
		}
		if filter != nil && !filterMatches(payload, filter) {
			continue
		}
		ptID, err := decodePointID(idKey, idKind)
		if err != nil {
			return nil, err
		}
		scored = append(scored, &candidate{
			score:   score,
			id:      ptID,
			payload: payload,
			vector:  ptVec,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	if limit > 0 && uint64(len(scored)) > limit {
		scored = scored[:limit]
	}
	out := make([]*pb.ScoredPoint, 0, len(scored))
	for _, c := range scored {
		out = append(out, &pb.ScoredPoint{
			Id:      c.id,
			Score:   c.score,
			Payload: c.payload,
			Vectors: c.vectorsOutput(),
		})
	}
	return out, nil
}

type candidate struct {
	score   float32
	id      *pb.PointId
	payload map[string]*pb.Value
	vector  []float32
}

func (c *candidate) vectorsOutput() *pb.VectorsOutput {
	if len(c.vector) == 0 {
		return nil
	}
	return &pb.VectorsOutput{
		VectorsOptions: &pb.VectorsOutput_Vector{
			Vector: &pb.VectorOutput{Data: c.vector},
		},
	}
}

// ── Scroll ───────────────────────────────────────────────────────────────────

// Scroll returns a page of points ordered by id_key.
func (s *Store) Scroll(ctx context.Context, limit uint32, offset *pb.PointId) ([]*pb.RetrievedPoint, *pb.PointId, error) {
	return s.scrollCollection(ctx, s.collection, limit, offset)
}

func (s *Store) scrollCollection(ctx context.Context, collection string, limit uint32, offset *pb.PointId) ([]*pb.RetrievedPoint, *pb.PointId, error) {
	tbl := tableName(collection)
	var after string
	if offset != nil {
		k, _, err := encodePointID(offset)
		if err != nil {
			return nil, nil, err
		}
		after = k
	}
	if limit == 0 {
		limit = 100
	}
	rows, err := s.conn.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id_key, id_kind, vector, payload FROM %s
		WHERE id_key > ?
		ORDER BY id_key ASC
		LIMIT ?
	`, tbl), after, int64(limit)+1)
	if err != nil {
		return nil, nil, fmt.Errorf("scroll %q: %w", collection, err)
	}
	defer rows.Close()
	var out []*pb.RetrievedPoint
	for rows.Next() {
		var idKey, idKind, payloadJSON string
		var vecBlob []byte
		if err := rows.Scan(&idKey, &idKind, &vecBlob, &payloadJSON); err != nil {
			return nil, nil, err
		}
		ptID, err := decodePointID(idKey, idKind)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, &pb.RetrievedPoint{
			Id:      ptID,
			Payload: decodePayload(payloadJSON),
			Vectors: vectorsOutputFromBlob(vecBlob),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *pb.PointId
	if uint32(len(out)) > limit {
		next = out[limit-1].Id
		out = out[:limit]
	}
	return out, next, nil
}

// ScrollFiltered scrolls the named collection applying the given filter.
func (s *Store) ScrollFiltered(ctx context.Context, collection string, filter *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error) {
	if collection == "" {
		collection = s.collection
	}
	if err := s.ensureCollection(ctx, collection, 0); err != nil {
		return nil, err
	}
	tbl := tableName(collection)
	rows, err := s.conn.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id_key, id_kind, vector, payload FROM %s
		WHERE id_key > ?
		ORDER BY id_key ASC
	`, tbl), offset)
	if err != nil {
		return nil, fmt.Errorf("scroll-filtered %q: %w", collection, err)
	}
	defer rows.Close()
	var out []*pb.RetrievedPoint
	for rows.Next() {
		var idKey, idKind, payloadJSON string
		var vecBlob []byte
		if err := rows.Scan(&idKey, &idKind, &vecBlob, &payloadJSON); err != nil {
			return nil, err
		}
		payload := decodePayload(payloadJSON)
		if filter != nil && !filterMatches(payload, filter) {
			continue
		}
		ptID, err := decodePointID(idKey, idKind)
		if err != nil {
			return nil, err
		}
		out = append(out, &pb.RetrievedPoint{
			Id:      ptID,
			Payload: payload,
			Vectors: vectorsOutputFromBlob(vecBlob),
		})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ── GetPoints ────────────────────────────────────────────────────────────────

func (s *Store) GetPoints(ctx context.Context, collection string, ids []*pb.PointId) ([]*pb.RetrievedPoint, error) {
	if collection == "" {
		collection = s.collection
	}
	if err := s.ensureCollection(ctx, collection, 0); err != nil {
		return nil, err
	}
	tbl := tableName(collection)
	rows, err := s.conn.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id_key, id_kind, vector, payload FROM %s WHERE id_key IN (`+placeholders(len(ids))+`)`, tbl),
		idValues(ids)...)
	if err != nil {
		return nil, fmt.Errorf("get points %q: %w", collection, err)
	}
	defer rows.Close()
	var out []*pb.RetrievedPoint
	for rows.Next() {
		var idKey, idKind, payloadJSON string
		var vecBlob []byte
		if err := rows.Scan(&idKey, &idKind, &vecBlob, &payloadJSON); err != nil {
			return nil, err
		}
		ptID, err := decodePointID(idKey, idKind)
		if err != nil {
			return nil, err
		}
		out = append(out, &pb.RetrievedPoint{
			Id:      ptID,
			Payload: decodePayload(payloadJSON),
			Vectors: vectorsOutputFromBlob(vecBlob),
		})
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return "''"
	}
	b := strings.Builder{}
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("?")
	}
	return b.String()
}

func idValues(ids []*pb.PointId) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		k, _, err := encodePointID(id)
		if err != nil {
			out = append(out, "")
			continue
		}
		out = append(out, k)
	}
	return out
}

// ── Delete ───────────────────────────────────────────────────────────────────

func (s *Store) DeleteBySource(ctx context.Context, sourceFile string) error {
	tbl := tableName(s.collection)
	_, err := s.conn.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE source_file = ?`, tbl), sourceFile)
	return err
}

func (s *Store) DeleteFiltered(ctx context.Context, collection string, filter *pb.Filter) error {
	if collection == "" {
		collection = s.collection
	}
	if err := s.ensureCollection(ctx, collection, 0); err != nil {
		return err
	}
	tbl := tableName(collection)
	if !filterIsSimpleSourceFile(filter) {
		return fmt.Errorf("embeddedstore: DeleteFiltered only supports source_file filters")
	}
	target := filterSourceFileValue(filter)
	_, err := s.conn.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE source_file = ?`, tbl), target)
	return err
}

// ── Counts ───────────────────────────────────────────────────────────────────

func (s *Store) Count(ctx context.Context) (uint64, error) {
	tbl := tableName(s.collection)
	var n int64
	if err := s.conn.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tbl)).Scan(&n); err != nil {
		return 0, err
	}
	return uint64(n), nil
}

func (s *Store) CountFiles(ctx context.Context) (int, error) {
	tbl := tableName(s.collection)
	row := s.conn.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(DISTINCT source_file) FROM %s WHERE source_file IS NOT NULL`, tbl))
	var n int64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return int(n), nil
}

// ── Indexes / health ────────────────────────────────────────────────────────

func (s *Store) CreatePayloadIndex(ctx context.Context, collection, field, fieldType string) error {
	// The embedded store keeps payloads in a single JSON column. Only the
	// source_file column has a real index; other field indexes are
	// informational and do not change the query plan. Returning nil keeps
	// the FactStore surface complete without requiring extra schema work.
	return nil
}

func (s *Store) Health(ctx context.Context) error {
	return s.conn.db.PingContext(ctx)
}

// ── SetPayload / UpdateVectors (stubs) ──────────────────────────────────────

// setPayload is a stub: the embedded store does not implement partial payload
// updates. The pruner code paths that need this remain on Qdrant.
func (s *sqlDB) setPayload(ctx context.Context, collection string, points []*pb.PointId, payload map[string]*pb.Value) error {
	return fmt.Errorf("embeddedstore: SetPayload is not implemented")
}

// updateVectors is a stub for the same reason.
func (s *sqlDB) updateVectors(ctx context.Context, collection string, points []*pb.PointVectors) error {
	return fmt.Errorf("embeddedstore: UpdateVectors is not implemented")
}

// ── Encoding helpers ────────────────────────────────────────────────────────

func encodePointID(id *pb.PointId) (key, kind string, err error) {
	if id == nil {
		return "", "", fmt.Errorf("nil point id")
	}
	switch v := id.PointIdOptions.(type) {
	case *pb.PointId_Uuid:
		return v.Uuid, "uuid", nil
	case *pb.PointId_Num:
		return fmt.Sprintf("%d", v.Num), "num", nil
	default:
		return "", "", fmt.Errorf("unknown point id kind")
	}
}

func decodePointID(key, kind string) (*pb.PointId, error) {
	switch kind {
	case "uuid":
		return &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: key}}, nil
	case "num":
		var n uint64
		if _, err := fmt.Sscanf(key, "%d", &n); err != nil {
			return nil, fmt.Errorf("decode num id: %w", err)
		}
		return &pb.PointId{PointIdOptions: &pb.PointId_Num{Num: n}}, nil
	default:
		return nil, fmt.Errorf("unknown point id kind %q", kind)
	}
}

func encodeVector(v *pb.Vectors) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	switch x := v.VectorsOptions.(type) {
	case *pb.Vectors_Vector:
		if x.Vector == nil {
			return nil, nil
		}
		buf := make([]byte, 4*len(x.Vector.Data))
		for i, f := range x.Vector.Data {
			binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
		}
		return buf, nil
	case *pb.Vectors_Vectors:
		return nil, fmt.Errorf("embeddedstore: named vectors are not supported")
	default:
		return nil, nil
	}
}

func decodeVector(blob []byte) []float32 {
	if len(blob)%4 != 0 {
		return nil
	}
	out := make([]float32, len(blob)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return out
}

func vectorsOutputFromBlob(blob []byte) *pb.VectorsOutput {
	if len(blob) == 0 {
		return nil
	}
	return &pb.VectorsOutput{
		VectorsOptions: &pb.VectorsOutput_Vector{
			Vector: &pb.VectorOutput{Data: decodeVector(blob)},
		},
	}
}

func encodePayload(p map[string]*pb.Value) (string, error) {
	if len(p) == 0 {
		return "{}", nil
	}
	flat := make(map[string]any, len(p))
	for k, v := range p {
		flat[k] = flattenValue(v)
	}
	b, err := json.Marshal(flat)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodePayload(raw string) map[string]*pb.Value {
	out := make(map[string]*pb.Value)
	if raw == "" {
		return out
	}
	var flat map[string]any
	if err := json.Unmarshal([]byte(raw), &flat); err != nil {
		return out
	}
	for k, v := range flat {
		out[k] = unflattenValue(v)
	}
	return out
}

func flattenValue(v *pb.Value) any {
	if v == nil {
		return nil
	}
	switch x := v.Kind.(type) {
	case *pb.Value_StringValue:
		return x.StringValue
	case *pb.Value_IntegerValue:
		return float64(x.IntegerValue)
	case *pb.Value_DoubleValue:
		return x.DoubleValue
	case *pb.Value_BoolValue:
		return x.BoolValue
	case *pb.Value_ListValue:
		out := make([]any, 0, len(x.ListValue.Values))
		for _, child := range x.ListValue.Values {
			out = append(out, flattenValue(child))
		}
		return out
	case *pb.Value_StructValue:
		out := make(map[string]any, len(x.StructValue.Fields))
		for k, child := range x.StructValue.Fields {
			out[k] = flattenValue(child)
		}
		return out
	default:
		return nil
	}
}

func unflattenValue(v any) *pb.Value {
	switch x := v.(type) {
	case string:
		return &pb.Value{Kind: &pb.Value_StringValue{StringValue: x}}
	case float64:
		return &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: x}}
	case bool:
		return &pb.Value{Kind: &pb.Value_BoolValue{BoolValue: x}}
	case []any:
		values := make([]*pb.Value, 0, len(x))
		for _, child := range x {
			values = append(values, unflattenValue(child))
		}
		return &pb.Value{Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: values}}}
	case map[string]any:
		fields := make(map[string]*pb.Value, len(x))
		for k, child := range x {
			fields[k] = unflattenValue(child)
		}
		return &pb.Value{Kind: &pb.Value_StructValue{StructValue: &pb.Struct{Fields: fields}}}
	default:
		return nil
	}
}

func stringPayload(p map[string]*pb.Value, key string) string {
	v, ok := p[key]
	if !ok || v == nil {
		return ""
	}
	if sv, ok := v.Kind.(*pb.Value_StringValue); ok {
		return sv.StringValue
	}
	return ""
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		fa, fb := float64(a[i]), float64(b[i])
		dot += fa * fb
		na += fa * fa
		nb += fb * fb
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// filterMatches applies a Qdrant filter to a payload. Supports the exact
// shape produced by Ragamuffin's call sites: keyword and text equality on
// nested Must conditions. Prefix-match and range conditions are not
// implemented because the embedded store is not the production query path.
func filterMatches(p map[string]*pb.Value, f *pb.Filter) bool {
	if f == nil {
		return true
	}
	for _, c := range f.Must {
		if !conditionMatches(p, c) {
			return false
		}
	}
	return true
}

func conditionMatches(p map[string]*pb.Value, c *pb.Condition) bool {
	if fc, ok := c.ConditionOneOf.(*pb.Condition_Field); ok {
		if !conditionFieldMatches(p, fc.Field) {
			return false
		}
	}
	return true
}

func conditionFieldMatches(p map[string]*pb.Value, fc *pb.FieldCondition) bool {
	if fc == nil {
		return true
	}
	if fc.Match == nil {
		return true
	}
	got := stringPayload(p, fc.Key)
	switch m := fc.Match.MatchValue.(type) {
	case *pb.Match_Keyword:
		return got == m.Keyword
	case *pb.Match_Text:
		return got == m.Text
	default:
		return true
	}
}

func filterIsSimpleSourceFile(f *pb.Filter) bool {
	if f == nil || len(f.Must) != 1 {
		return false
	}
	fc, ok := f.Must[0].ConditionOneOf.(*pb.Condition_Field)
	if !ok || fc.Field.Key != "source_file" || fc.Field.Match == nil {
		return false
	}
	_, ok = fc.Field.Match.MatchValue.(*pb.Match_Keyword)
	return ok
}

func filterSourceFileValue(f *pb.Filter) string {
	if f == nil || len(f.Must) == 0 {
		return ""
	}
	fc, _ := f.Must[0].ConditionOneOf.(*pb.Condition_Field)
	if fc == nil || fc.Field.Match == nil {
		return ""
	}
	if m, ok := fc.Field.Match.MatchValue.(*pb.Match_Keyword); ok {
		return m.Keyword
	}
	return ""
}
