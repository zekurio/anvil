package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/flow"
)

// DefaultFlowName names the flow a fresh installation starts with.
const DefaultFlowName = "Default AV1"

var (
	// ErrRevisionConflict reports an update based on a revision that is no
	// longer the stored one: someone else saved in between.
	ErrRevisionConflict = errors.New("store: the stored revision is newer")
	// ErrNameTaken reports a create or rename onto a name already in use.
	ErrNameTaken = errors.New("store: the name is already in use")
	// ErrInUse reports a delete refused because libraries still reference the
	// target. The error wrapping it is an *InUseError naming them.
	ErrInUse = errors.New("store: still in use")
)

// InUseError names the libraries that keep a flow or an Arr connection from
// being deleted.
type InUseError struct {
	Libraries []string
}

func (e *InUseError) Error() string {
	return "still used by " + quotedList(e.Libraries)
}

func (e *InUseError) Unwrap() error {
	return ErrInUse
}

func quotedList(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	switch len(quoted) {
	case 0:
		return "nothing"
	case 1:
		return "library " + quoted[0]
	default:
		return "libraries " + strings.Join(quoted, ", ")
	}
}

// Flow is a stored flow. Revision starts at 1 and grows by one with every
// update, which is what makes concurrent edits detectable.
type Flow struct {
	ID          int64
	Name        string
	Description string
	Revision    int
	Graph       flow.Graph
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// FlowInput is what a caller sets on a flow. The store keeps the graph as it
// is given; deciding whether it may be saved is the caller's job.
type FlowInput struct {
	Name        string
	Description string
	Graph       flow.Graph
}

const flowColumns = `id, name, description, revision, graph_json, created_at, updated_at`

func (s *SQLiteStore) ListFlows(ctx context.Context) (flows []Flow, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+flowColumns+` FROM flows ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list flows: %w", err)
	}
	defer closeRows(rows, &err, "close flows")
	flows = []Flow{}
	for rows.Next() {
		item, err := scanFlow(rows)
		if err != nil {
			return nil, err
		}
		flows = append(flows, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate flows: %w", err)
	}
	return flows, nil
}

func (s *SQLiteStore) GetFlow(ctx context.Context, id int64) (Flow, error) {
	return getFlow(ctx, s.db, id)
}

func getFlow(ctx context.Context, q rowQuerier, id int64) (Flow, error) {
	return scanFlow(q.QueryRowContext(ctx, `SELECT `+flowColumns+` FROM flows WHERE id = ?`, id))
}

func (s *SQLiteStore) CreateFlow(ctx context.Context, input FlowInput, now time.Time) (Flow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Flow{}, fmt.Errorf("begin flow creation: %w", err)
	}
	defer rollback(tx)
	created, err := createFlowTx(ctx, tx, input, defaultNow(now))
	if err != nil {
		return Flow{}, err
	}
	if err := tx.Commit(); err != nil {
		return Flow{}, fmt.Errorf("commit flow creation: %w", err)
	}
	return created, nil
}

func createFlowTx(ctx context.Context, tx *sql.Tx, input FlowInput, now time.Time) (Flow, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Flow{}, errors.New("flow name is required")
	}
	if err := flowNameFreeTx(ctx, tx, name, 0); err != nil {
		return Flow{}, err
	}
	graph, err := encodeGraph(input.Graph)
	if err != nil {
		return Flow{}, err
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO flows (name, description, revision, graph_json, created_at, updated_at)
VALUES (?, ?, 1, ?, ?, ?)
`, name, input.Description, graph, encodeTime(now), encodeTime(now))
	if err != nil {
		return Flow{}, fmt.Errorf("create flow %q: %w", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Flow{}, fmt.Errorf("flow id: %w", err)
	}
	return getFlow(ctx, tx, id)
}

// UpdateFlow replaces a flow, provided expectedRevision is still the stored
// one. It returns ErrRevisionConflict otherwise, so two editors cannot
// overwrite each other without noticing.
func (s *SQLiteStore) UpdateFlow(ctx context.Context, id int64, input FlowInput, expectedRevision int, now time.Time) (Flow, error) {
	now = defaultNow(now)
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Flow{}, errors.New("flow name is required")
	}
	graph, err := encodeGraph(input.Graph)
	if err != nil {
		return Flow{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Flow{}, fmt.Errorf("begin flow update: %w", err)
	}
	defer rollback(tx)

	current, err := getFlow(ctx, tx, id)
	if err != nil {
		return Flow{}, err
	}
	if current.Revision != expectedRevision {
		return Flow{}, fmt.Errorf("%w: flow %q is at revision %d, the update was based on revision %d", ErrRevisionConflict, current.Name, current.Revision, expectedRevision)
	}
	if err := flowNameFreeTx(ctx, tx, name, id); err != nil {
		return Flow{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE flows
SET name = ?, description = ?, graph_json = ?, revision = revision + 1, updated_at = ?
WHERE id = ? AND revision = ?
`, name, input.Description, graph, encodeTime(now), id, expectedRevision); err != nil {
		return Flow{}, fmt.Errorf("update flow %d: %w", id, err)
	}
	updated, err := getFlow(ctx, tx, id)
	if err != nil {
		return Flow{}, err
	}
	if err := tx.Commit(); err != nil {
		return Flow{}, fmt.Errorf("commit flow update: %w", err)
	}
	return updated, nil
}

// DeleteFlow removes a flow no library is assigned to. A flow in use is
// refused with an *InUseError naming the libraries.
func (s *SQLiteStore) DeleteFlow(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin flow deletion: %w", err)
	}
	defer rollback(tx)
	if _, err := getFlow(ctx, tx, id); err != nil {
		return err
	}
	libraries, err := libraryNamesTx(ctx, tx, `flow_id = ?`, id)
	if err != nil {
		return err
	}
	if len(libraries) > 0 {
		return &InUseError{Libraries: libraries}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM flows WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete flow %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit flow deletion: %w", err)
	}
	return nil
}

// FlowNames maps every flow to its name, without loading any graph.
func (s *SQLiteStore) FlowNames(ctx context.Context) (map[int64]string, error) {
	names := make(map[int64]string)
	err := s.eachRow(ctx, "flow names", `SELECT id, name FROM flows`, nil, func(rows *sql.Rows) error {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		names[id] = name
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}

// FlowLibraries maps every flow in use to the names of the libraries assigned
// to it, sorted.
func (s *SQLiteStore) FlowLibraries(ctx context.Context) (usage map[int64][]string, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT flow_id, name FROM libraries ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list flow usage: %w", err)
	}
	defer closeRows(rows, &err, "close flow usage")
	usage = make(map[int64][]string)
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan flow usage: %w", err)
		}
		usage[id] = append(usage[id], name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate flow usage: %w", err)
	}
	return usage, nil
}

// seedDefaultFlow gives a database its starter flow, once. The marker rather
// than the flow's presence decides, so an operator who deletes or renames the
// flow does not get it back.
func (s *SQLiteStore) seedDefaultFlow(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin default flow seed: %w", err)
	}
	defer rollback(tx)
	if _, seeded, err := getMeta(ctx, tx, metaDefaultFlowSeeded); err != nil || seeded {
		return err
	}
	now := time.Now().UTC()
	if _, err := createFlowTx(ctx, tx, FlowInput{
		Name:        DefaultFlowName,
		Description: "Searches for an AV1 encode at the default quality target, crops black bars, and keeps every audio and subtitle track.",
		Graph:       flow.Default(),
	}, now); err != nil && !errors.Is(err, ErrNameTaken) {
		return fmt.Errorf("seed default flow: %w", err)
	}
	if err := setMeta(ctx, tx, metaDefaultFlowSeeded, encodeTime(now)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit default flow seed: %w", err)
	}
	return nil
}

func flowNameFreeTx(ctx context.Context, tx *sql.Tx, name string, exceptID int64) error {
	var taken int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM flows WHERE name = ? AND id != ?`, name, exceptID).Scan(&taken); err != nil {
		return fmt.Errorf("check flow name: %w", err)
	}
	if taken > 0 {
		return fmt.Errorf("%w: a flow named %q already exists", ErrNameTaken, name)
	}
	return nil
}

func encodeGraph(graph flow.Graph) ([]byte, error) {
	if graph.Nodes == nil {
		graph.Nodes = []flow.Node{}
	}
	if graph.Edges == nil {
		graph.Edges = []flow.Edge{}
	}
	data, err := json.Marshal(graph)
	if err != nil {
		return nil, fmt.Errorf("encode flow graph: %w", err)
	}
	return data, nil
}

func scanFlow(row scanner) (Flow, error) {
	var item Flow
	var graph []byte
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.Name, &item.Description, &item.Revision, &graph, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Flow{}, ErrNotFound
	}
	if err != nil {
		return Flow{}, fmt.Errorf("scan flow: %w", err)
	}
	if err := json.Unmarshal(graph, &item.Graph); err != nil {
		return Flow{}, fmt.Errorf("decode graph of flow %d: %w", item.ID, err)
	}
	if item.Graph.Nodes == nil {
		item.Graph.Nodes = []flow.Node{}
	}
	if item.Graph.Edges == nil {
		item.Graph.Edges = []flow.Edge{}
	}
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}
