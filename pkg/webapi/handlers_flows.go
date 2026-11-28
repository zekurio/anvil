package webapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/store"
)

// maxFlowNameLength keeps a flow name short enough to show in a list.
const maxFlowNameLength = 80

func (s *Server) listFlows(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	flows, err := s.Store.ListFlows(r.Context())
	if err != nil {
		return fail(err)
	}
	usage, err := s.Store.FlowLibraries(r.Context())
	if err != nil {
		return fail(err)
	}
	list := FlowList{Flows: make([]FlowSummary, 0, len(flows))}
	for _, stored := range flows {
		list.Flows = append(list.Flows, flowSummary(stored, usage[stored.ID]))
	}
	return ok(list)
}

func flowSummary(stored store.Flow, libraries []string) FlowSummary {
	if libraries == nil {
		libraries = []string{}
	}
	return FlowSummary{
		ID:          stored.ID,
		Name:        stored.Name,
		Description: stored.Description,
		Revision:    stored.Revision,
		NodeCount:   len(stored.Graph.Nodes),
		Libraries:   libraries,
		CreatedAt:   stored.CreatedAt,
		UpdatedAt:   stored.UpdatedAt,
	}
}

func (s *Server) flowResponse(ctx context.Context, stored store.Flow) (Flow, error) {
	usage, err := s.Store.FlowLibraries(ctx)
	if err != nil {
		return Flow{}, err
	}
	graph := normalizeGraph(stored.Graph)
	return Flow{FlowSummary: flowSummary(stored, usage[stored.ID]), Graph: graph, Check: flowCheck(graph)}, nil
}

// flowID reads the {id} of a flow route.
func flowID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, notFoundf("There is no flow %q.", r.PathValue("id"))
	}
	return id, nil
}

func (s *Server) storedFlow(ctx context.Context, id int64) (store.Flow, error) {
	stored, err := s.Store.GetFlow(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Flow{}, notFoundf("There is no flow %d.", id)
	}
	return stored, err
}

func (s *Server) getFlow(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	id, err := flowID(r)
	if err != nil {
		return fail(err)
	}
	stored, err := s.storedFlow(r.Context(), id)
	if err != nil {
		return fail(err)
	}
	response, err := s.flowResponse(r.Context(), stored)
	if err != nil {
		return fail(err)
	}
	return ok(response)
}

// flowInput validates what a create or update carries. A graph with error
// issues is refused with every issue, so the editor can mark the nodes.
func flowInput(write FlowWrite) (store.FlowInput, error) {
	name := strings.TrimSpace(write.Name)
	switch {
	case name == "":
		return store.FlowInput{}, invalidf("name", "A flow needs a name.")
	case utf8.RuneCountInString(name) > maxFlowNameLength:
		return store.FlowInput{}, invalidf("name", "A flow name can have at most %d characters.", maxFlowNameLength)
	}
	graph := normalizeGraph(write.Graph)
	if check := flowCheck(graph); !check.Valid {
		return store.FlowInput{}, &apiError{
			status: http.StatusUnprocessableEntity, code: CodeInvalidFlow, issues: check.Issues,
			message: "Fix the problems in this flow before saving it.",
		}
	}
	return store.FlowInput{Name: name, Description: strings.TrimSpace(write.Description), Graph: graph}, nil
}

func (s *Server) createFlow(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	var write FlowWrite
	if err := decodeBody(r, &write); err != nil {
		return fail(err)
	}
	input, err := flowInput(write)
	if err != nil {
		return fail(err)
	}
	s.mutations.Lock()
	defer s.mutations.Unlock()
	stored, err := s.Store.CreateFlow(r.Context(), input, s.now())
	if errors.Is(err, store.ErrNameTaken) {
		return fail(flowNameTaken(input.Name))
	}
	if err != nil {
		return fail(err)
	}
	response, err := s.flowResponse(r.Context(), stored)
	if err != nil {
		return fail(err)
	}
	return created(response)
}

func flowNameTaken(name string) error {
	return nameTakenf("Another flow is already named %q. Choose another name.", name)
}

func (s *Server) updateFlow(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	id, err := flowID(r)
	if err != nil {
		return fail(err)
	}
	var write FlowWrite
	if err := decodeBody(r, &write); err != nil {
		return fail(err)
	}
	input, err := flowInput(write)
	if err != nil {
		return fail(err)
	}
	s.mutations.Lock()
	defer s.mutations.Unlock()
	stored, err := s.Store.UpdateFlow(r.Context(), id, input, write.Revision, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fail(notFoundf("There is no flow %d.", id))
	case errors.Is(err, store.ErrRevisionConflict):
		return fail(conflictf("This flow was changed elsewhere. Reload it and apply your changes again."))
	case errors.Is(err, store.ErrNameTaken):
		return fail(flowNameTaken(input.Name))
	case err != nil:
		return fail(err)
	}
	// Running attempts keep the snapshot they started with; the next attempt
	// of every job reads the flow again, so nothing has to be refreshed.
	response, err := s.flowResponse(r.Context(), stored)
	if err != nil {
		return fail(err)
	}
	return ok(response)
}

func (s *Server) deleteFlow(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	id, err := flowID(r)
	if err != nil {
		return fail(err)
	}
	s.mutations.Lock()
	defer s.mutations.Unlock()
	stored, err := s.storedFlow(r.Context(), id)
	if err != nil {
		return fail(err)
	}
	err = s.Store.DeleteFlow(r.Context(), id)
	var inUse *store.InUseError
	switch {
	case errors.As(err, &inUse):
		return fail(conflictf("Flow %q is %s. Assign another flow there first.", stored.Name, inUse.Error()))
	case errors.Is(err, store.ErrNotFound):
		return fail(notFoundf("There is no flow %d.", id))
	case err != nil:
		return fail(err)
	}
	return noContent()
}

func (s *Server) duplicateFlow(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	id, err := flowID(r)
	if err != nil {
		return fail(err)
	}
	s.mutations.Lock()
	defer s.mutations.Unlock()
	source, err := s.storedFlow(r.Context(), id)
	if err != nil {
		return fail(err)
	}
	names, err := s.Store.FlowNames(r.Context())
	if err != nil {
		return fail(err)
	}
	taken := make(map[string]bool, len(names))
	for _, name := range names {
		taken[name] = true
	}
	name := source.Name + " copy"
	for copyNumber := 2; taken[name]; copyNumber++ {
		name = fmt.Sprintf("%s copy %d", source.Name, copyNumber)
	}
	stored, err := s.Store.CreateFlow(r.Context(), store.FlowInput{Name: name, Description: source.Description, Graph: source.Graph}, s.now())
	if err != nil {
		return fail(err)
	}
	response, err := s.flowResponse(r.Context(), stored)
	if err != nil {
		return fail(err)
	}
	return created(response)
}

func (s *Server) checkFlow(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	var request FlowCheckRequest
	if err := decodeBody(r, &request); err != nil {
		return fail(err)
	}
	return ok(flowCheck(normalizeGraph(request.Graph)))
}

// flowIssues lists the error issues of a stored flow in one sentence, for a
// message about why it cannot be assigned.
func flowIssues(graph flow.Graph) string {
	var messages []string
	for _, issue := range flow.Validate(graph) {
		if issue.Severity == flow.SeverityError {
			messages = append(messages, issue.Message)
		}
	}
	return strings.Join(messages, " ")
}
