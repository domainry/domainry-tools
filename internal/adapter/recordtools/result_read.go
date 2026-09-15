package records

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/domainry/domainry-knowledge/contract"
	sdk "github.com/domainry/domainry-tools-sdk"
	"github.com/domainry/domainry-tools-sdk/schema"
)

// AuthorizeResultRead reads immutable versions and command receipts under
// current data permissions. It never invokes, reconciles, saves or relists.
// Record ownership stays personal; a published reference cannot change it.
func (a *Adapter) AuthorizeResultRead(ctx context.Context, in sdk.Request, out sdk.Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.Store == nil || a.Authorize == nil || a.Spec.Validate == nil || !recordAuthorityValid(in.Authority) || out.Status != "completed" || out.ErrorCode != "" || out.Completion != "" || in.OutcomeInspectionToken != "" {
		return recordReadDenied("source_invalid")
	}
	var definition sdk.Definition
	for _, d := range Definitions(a.Spec) {
		if d.Key == in.Definition.Key {
			definition = d
			break
		}
	}
	expected, err := json.Marshal(definition)
	actual, marshalErr := json.Marshal(in.Definition)
	if err != nil || marshalErr != nil || definition.Key == "" || !bytes.Equal(expected, actual) || in.Call.Name != definition.Key || len(out.Content) > definition.MaxOutputBytes {
		return recordReadDenied("source_invalid")
	}
	inputSchema, err := schema.CompileSchema(definition.InputSchema)
	if err != nil || schema.ValidateJSON(inputSchema, []byte(in.Call.Arguments)) != nil {
		return recordReadDenied("source_invalid")
	}
	producer := in.Authority
	if in.ResultProducer != nil {
		producer = *in.ResultProducer
	}
	if !recordAuthorityValid(producer) || producer.RuntimeID != in.Authority.RuntimeID || producer.WorkspaceID != in.Authority.WorkspaceID || producer.UserID != in.Authority.UserID {
		return recordReadDenied("owner_mismatch")
	}
	key, readArgs := a.Spec.Prefix+"_read", ""
	var saved []contract.Record
	switch definition.Key {
	case a.Spec.Prefix + "_list":
		var q struct {
			Query, Cursor string
			Limit         int
		}
		var page contract.Page
		if recordDecode([]byte(in.Call.Arguments), &q) != nil || recordDecode(out.Content, &page) != nil || page.Items == nil || out.ResourceID != "" {
			return recordReadDenied("source_invalid")
		}
		if q.Limit == 0 {
			q.Limit = 5
		}
		if len(page.Items) > q.Limit || page.Complete && page.NextCursor != "" || !page.Complete && (len(page.Items) != q.Limit || page.NextCursor != page.Items[len(page.Items)-1].ID) {
			return recordReadDenied("source_invalid")
		}
		previous := q.Cursor
		for _, row := range page.Items {
			if row.ID <= previous || q.Query != "" && !strings.Contains(strings.ToLower(row.Title+" "+string(row.Data)), strings.ToLower(q.Query)) {
				return recordReadDenied("source_invalid")
			}
			previous = row.ID
		}
		key, readArgs, saved = definition.Key, in.Call.Arguments, page.Items
	case a.Spec.Prefix + "_read":
		var q struct {
			ID       string
			Revision int64
		}
		var row contract.Record
		if recordDecode([]byte(in.Call.Arguments), &q) != nil || recordDecode(out.Content, &row) != nil || out.ResourceID != "" || row.ID != q.ID || q.Revision > 0 && row.Revision != q.Revision {
			return recordReadDenied("source_invalid")
		}
		readArgs, err = recordReadArguments(row)
		saved = []contract.Record{row}
	case a.Spec.Prefix + "_save":
		var row contract.Record
		if recordDecode(out.Content, &row) != nil || out.ResourceID != row.ID || strings.TrimSpace(in.IdempotencyKey) == "" {
			return recordReadDenied("source_invalid")
		}
		w, mutationErr := a.mutation(in)
		if mutationErr != nil || w.ID != "" && row.ID != w.ID || row.Revision != w.ExpectedRevision+1 {
			return recordReadDenied("source_invalid")
		}
		readArgs, err = recordReadArguments(row)
		saved = []contract.Record{row}
	default:
		return recordReadDenied("source_invalid")
	}
	if err != nil {
		return err
	}
	if err := a.authorizeRecordRead(ctx, in, in.Authority, key, readArgs); err != nil {
		return err
	}
	if producer != in.Authority {
		if err := a.authorizeRecordRead(ctx, in, producer, key, readArgs); err != nil {
			return err
		}
	}
	for _, row := range saved {
		if row.Kind != a.Spec.Kind || row.ID == "" || row.Revision < 1 {
			return recordReadDenied("source_invalid")
		}
		current, err := a.Store.Get(ctx, a.Spec.Kind, row.ID, row.Revision, in.Authority)
		if err != nil {
			return err
		}
		if !sameRecord(current, row) {
			return recordReadDenied("source_changed")
		}
	}
	if definition.Effect == "write" {
		w, err := a.mutation(in)
		if err != nil {
			return err
		}
		original, found, err := a.Store.Receipt(ctx, a.Spec.Kind, w, producer)
		if err != nil {
			return err
		}
		if !found || !sameRecord(original, saved[0]) {
			return recordReadDenied("receipt_changed")
		}
	}
	// Recheck both live roles after every source read, including an old receipt
	// lookup. Neither a saved version nor producer lookup freezes reader rights.
	if producer != in.Authority {
		if err := a.authorizeRecordRead(ctx, in, producer, key, readArgs); err != nil {
			return err
		}
	}
	return a.authorizeRecordRead(ctx, in, in.Authority, key, readArgs)
}

func (a *Adapter) authorizeRecordRead(ctx context.Context, original sdk.Request, actor sdk.Authority, key, args string) error {
	var definition sdk.Definition
	for _, d := range Definitions(a.Spec) {
		if d.Key == key {
			definition = d
			break
		}
	}
	request := sdk.Request{Authority: actor, ConversationID: original.ConversationID, RunID: original.RunID, CorrelationID: original.CorrelationID, Step: original.Step, Definition: definition, Call: sdk.Call{ID: original.Call.ID, Name: key, Arguments: args}}
	auth, err := a.Authorize(ctx, request)
	if err != nil {
		return err
	}
	if !auth.Granted || auth.ConfirmationRequired {
		return recordReadDenied("read_access_denied")
	}
	return ctx.Err()
}

func recordReadArguments(row contract.Record) (string, error) {
	raw, err := json.Marshal(struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}{row.ID, row.Revision})
	return string(raw), err
}

func recordDecode(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return recordReadDenied("source_invalid")
	}
	return nil
}

func sameRecord(a, b contract.Record) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

func recordAuthorityValid(a sdk.Authority) bool {
	return a.Known && a.RuntimeID != "" && a.WorkspaceID != "" && a.UserID != ""
}

func recordReadDenied(code string) error {
	return &sdk.Error{Class: "forbidden", Code: "tool.record." + code}
}
