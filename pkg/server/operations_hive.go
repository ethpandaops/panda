package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ethpandaops/panda/pkg/hive"
	"github.com/ethpandaops/panda/pkg/operations"
)

// hiveOperations are the hive.* operations, each taking the decoded args.
var hiveOperations = map[string]func(s *service, ctx context.Context, args map[string]any) (any, error){
	"hive.list_groups": (*service).hiveListGroups,
	"hive.get_summary": func(s *service, ctx context.Context, args map[string]any) (any, error) {
		group, err := requiredStringArg(args, "group")
		if err != nil {
			return nil, hiveArgError{err}
		}

		return s.hiveStore().Summary(ctx, group)
	},
	"hive.get_failures": func(s *service, ctx context.Context, args map[string]any) (any, error) {
		group, err := requiredStringArg(args, "group")
		if err != nil {
			return nil, hiveArgError{err}
		}

		return s.hiveStore().Failures(ctx, group, hiveFilter(args, ""))
	},
	"hive.list_tests": func(s *service, ctx context.Context, args map[string]any) (any, error) {
		group, err := requiredStringArg(args, "group")
		if err != nil {
			return nil, hiveArgError{err}
		}

		return s.hiveStore().Tests(ctx, group, hiveFilter(args, "fail"),
			optionalIntArg(args, "offset", 0), optionalIntArg(args, "limit", 50))
	},
	"hive.search": func(s *service, ctx context.Context, args map[string]any) (any, error) {
		group, err := requiredStringArg(args, "group")
		if err != nil {
			return nil, hiveArgError{err}
		}

		query, err := requiredStringArg(args, "query")
		if err != nil {
			return nil, hiveArgError{err}
		}

		return s.hiveStore().Search(ctx, group, query, hiveFilter(args, ""), optionalIntArg(args, "limit", 50))
	},
	"hive.get_test": func(s *service, ctx context.Context, args map[string]any) (any, error) {
		group, file, id, err := hiveTestArgs(args)
		if err != nil {
			return nil, err
		}

		fullLog, err := optionalBoolArg(args, "full_log")
		if err != nil {
			return nil, hiveArgError{err}
		}

		return s.hiveStore().Detail(ctx, group, file, id, fullLog != nil && *fullLog)
	},
	"hive.find_test": func(s *service, ctx context.Context, args map[string]any) (any, error) {
		group, err := requiredStringArg(args, "group")
		if err != nil {
			return nil, hiveArgError{err}
		}

		filter := hiveFilter(args, "")
		if filter.Pattern == "" {
			return nil, hiveArgError{errors.New("filter is required")}
		}

		fullLog, err := optionalBoolArg(args, "full_log")
		if err != nil {
			return nil, hiveArgError{err}
		}

		return s.hiveStore().Find(ctx, group, filter, fullLog != nil && *fullLog)
	},
	"hive.get_diff": func(s *service, ctx context.Context, args map[string]any) (any, error) {
		group, err := requiredStringArg(args, "group")
		if err != nil {
			return nil, hiveArgError{err}
		}

		withTests, err := optionalBoolArg(args, "include_tests")
		if err != nil {
			return nil, hiveArgError{err}
		}

		return s.hiveStore().Diff(ctx, group, withTests != nil && *withTests)
	},
	"hive.link_test": func(s *service, _ context.Context, args map[string]any) (any, error) {
		group, file, id, err := hiveTestArgs(args)
		if err != nil {
			return nil, err
		}

		return map[string]any{"url": s.hiveStore().TestURL(group, file, id)}, nil
	},
}

// hiveArgError marks an error in the caller's arguments.
type hiveArgError struct{ error }

func (s *service) handleHiveOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	run, ok := hiveOperations[operationID]
	if !ok {
		return false
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return true
	}

	data, err := run(s, r.Context(), req.Args)
	if err != nil {
		writeAPIError(w, hiveErrorStatus(err), err.Error())

		return true
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: data,
	})

	return true
}

func hiveErrorStatus(err error) int {
	var argErr hiveArgError

	switch {
	case errors.As(err, &argErr):
		return http.StatusBadRequest
	case errors.Is(err, hive.ErrUnknownGroup), errors.Is(err, hive.ErrTestNotFound):
		return http.StatusNotFound
	default:
		return http.StatusBadGateway
	}
}

// hiveStore returns the store, built on first use.
func (s *service) hiveStore() *hive.Store {
	s.hiveOnce.Do(func() {
		if s.hiveInst == nil {
			s.hiveInst = hive.NewStore(hive.DefaultBaseURL, &http.Client{Timeout: 2 * time.Minute})
		}
	})

	return s.hiveInst
}

func (s *service) hiveListGroups(ctx context.Context, _ map[string]any) (any, error) {
	groups, err := s.hiveStore().Groups(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		items = append(items, map[string]any{
			"name": group,
			"type": "hive",
			"url":  fmt.Sprintf("%s/#/group/%s", hive.DefaultBaseURL, group),
		})
	}

	return map[string]any{"groups": items}, nil
}

// hiveFilter reads the client, simulator, status and filter args. status
// "all" means both passing and failing tests.
func hiveFilter(args map[string]any, defaultStatus string) hive.Filter {
	status := optionalStringArg(args, "status")

	switch status {
	case "":
		status = defaultStatus
	case "all":
		status = ""
	}

	return hive.Filter{
		Client:    optionalStringArg(args, "client"),
		Simulator: optionalStringArg(args, "simulator"),
		Status:    status,
		Pattern:   optionalStringArg(args, "filter"),
	}
}

func hiveTestArgs(args map[string]any) (group, file, id string, err error) {
	for key, value := range map[string]*string{"group": &group, "suite_file": &file, "test_id": &id} {
		if *value, err = requiredStringArg(args, key); err != nil {
			return "", "", "", hiveArgError{err}
		}
	}

	return group, file, id, nil
}
