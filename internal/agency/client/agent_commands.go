package agencyclient

import (
	"context"
	"errors"
	"io"

	"github.com/JayWanX/memory/internal/agency"
)

const maxIntentInputBytes = agency.MaxIntentCanonicalBytes

type captureStatus struct {
	ByteSize int64  `json:"byte_size"`
	Handle   string `json:"handle"`
	Schema   string `json:"schema"`
	Version  int    `json:"version"`
}

func (app *terminal) runCurrent(ctx context.Context, store *journalStore, client agencyClient) int {
	var projection []byte
	err := store.withLock(false, func(directory *lockedJournalDirectory) error {
		journal, err := directory.load()
		if err != nil {
			return err
		}
		if validTerminalName(journal.fileName) {
			journal.clear()
			if !journal.CurrentOperation.IsZero() {
				return newControlError(codeOperationPending,
					"an accepted R7 receipt is awaiting presentation")
			}
			return newControlError(codeOperationPending,
				"an accepted R7 receipt is awaiting Host-boundary reconciliation")
		}
		defer journal.clear()
		if journal.CurrentOperation.IsZero() {
			operation, err := newCurrentOperation(app.deps.random)
			if err != nil {
				return err
			}
			journal.CurrentOperation = operation
			if err := directory.write(journal); err != nil {
				return err
			}
		}
		view, apiErr := client.Current(ctx, journal.Attachment, journal.CurrentOperation.String())
		if apiErr != nil {
			return apiErr
		}
		currentProjection, err := classifyCurrentProjection(view)
		if err != nil {
			return err
		}
		journal.CurrentProjection = currentProjection
		if err := directory.write(journal); err != nil {
			return err
		}
		projection = append([]byte(nil), view...)
		return nil
	})
	if err != nil {
		return app.writeCommandError(err)
	}
	return app.writeCanonicalProjection(projection)
}

func classifyCurrentProjection(view []byte) (string, error) {
	hasCurrent, err := agency.AgentViewProjectionHasCurrent(view)
	if err != nil {
		return "", errors.New("R7 Current response is invalid")
	}
	if !hasCurrent {
		return currentProjectionEmpty, nil
	}
	return currentProjectionSubject, nil
}

func (app *terminal) runCapture(ctx context.Context, store *journalStore, client agencyClient) int {
	content, apiErr := readBoundedInput(app.stdin, maxArtifactInputBytes,
		codeArtifactTooLarge, "Artifact input exceeds its closed byte bound")
	if apiErr != nil {
		return app.writeError(apiErr)
	}
	defer clear(content)
	var captured artifactCapture
	err := store.withLock(false, func(directory *lockedJournalDirectory) error {
		journal, err := directory.load()
		if err != nil {
			return err
		}
		defer journal.clear()
		if validTerminalName(journal.fileName) {
			return newControlError(codeOperationPending,
				"an accepted R7 receipt is awaiting presentation")
		}
		capture, apiErr := client.Capture(ctx, content)
		if apiErr != nil {
			return apiErr
		}
		if err := journal.addCandidate(capture); err != nil {
			return err
		}
		if err := directory.write(journal); err != nil {
			return err
		}
		captured = capture
		return nil
	})
	if err != nil {
		return app.writeCommandError(err)
	}
	return app.writeJSON(captureStatus{Schema: "memory.artifact.capture", Version: 1,
		Handle: captured.Handle, ByteSize: captured.ByteSize})
}

func (app *terminal) runReadArtifact(ctx context.Context, store *journalStore, client agencyClient,
	handle string,
) int {
	var content []byte
	err := store.withLock(false, func(directory *lockedJournalDirectory) error {
		journal, err := directory.load()
		if err != nil {
			return err
		}
		defer journal.clear()
		if validTerminalName(journal.fileName) {
			return newControlError(codeOperationPending,
				"an accepted R7 receipt is awaiting presentation")
		}
		if journal.CurrentOperation.IsZero() {
			return newControlError(codeContextRequired,
				"agent current must establish a bounded View before Artifact read")
		}
		read, apiErr := client.ReadArtifact(ctx, journal.Attachment,
			journal.CurrentOperation.String(), handle)
		if apiErr != nil {
			return apiErr
		}
		content = append([]byte(nil), read...)
		clear(read)
		return nil
	})
	if err != nil {
		clear(content)
		return app.writeCommandError(err)
	}
	defer clear(content)
	if _, err := app.stdout.Write(content); err != nil {
		return 1
	}
	return 0
}

func (app *terminal) runSubmit(ctx context.Context, store *journalStore, client agencyClient) int {
	raw, apiErr := readBoundedInput(app.stdin, maxIntentInputBytes,
		codeContentTooLarge, "Intent input exceeds its closed byte bound")
	if apiErr != nil {
		if apiErr.Code == codeContentRequired {
			return app.writeError(newControlError(codeContentRequired,
				"provide exactly one Intent JSON object on stdin with a quoted heredoc"))
		}
		return app.writeError(apiErr)
	}
	defer clear(raw)
	intent, err := agency.ParseAgentIntentJSON(raw)
	if err != nil {
		return app.writeError(intentInputControlError(err))
	}
	var receipt []byte
	var terminal clientJournal
	err = store.withLock(false, func(directory *lockedJournalDirectory) error {
		journal, err := directory.load()
		if err != nil {
			return err
		}
		defer journal.clear()
		if journal.CurrentOperation.IsZero() {
			return newControlError(codeContextRequired,
				"agent current must establish a bounded View before submit")
		}
		candidates, err := journal.bindCandidates(intent)
		if err != nil {
			return candidateBindingControlError(err)
		}
		operation, err := deriveAdmissionOperation(journal.CurrentOperation, intent, candidates)
		if err != nil {
			return err
		}
		if validTerminalName(journal.fileName) {
			expected, err := terminalOperation(journal.fileName)
			if err != nil || expected != operation {
				return newControlError(codeOperationMismatch,
					"terminal R7 replay requires the exact prior Intent")
			}
		}
		projected, apiErr := client.Submit(ctx, journal.Attachment, journal.CurrentOperation.String(),
			operation.String(), intent.CanonicalJSON(), candidates)
		if apiErr != nil {
			return apiErr
		}
		outcome, err := agentReceiptOutcome(projected)
		if err != nil {
			return err
		}
		if validTerminalName(journal.fileName) && outcome != "accepted" {
			return errors.New("terminal R7 replay returned a non-accepted receipt")
		}
		if outcome == "accepted" && journal.fileName == journalActiveName {
			terminal, err = directory.markTerminal(journal, operation)
			if err != nil {
				return err
			}
		} else if validTerminalName(journal.fileName) {
			terminal = journal
			terminal.Attachment.Credential = append([]byte(nil), journal.Attachment.Credential...)
		}
		receipt = append([]byte(nil), projected...)
		return nil
	})
	if err != nil {
		terminal.clear()
		return app.writeCommandError(err)
	}
	if app.writeCanonicalProjection(receipt) != 0 {
		terminal.clear()
		return 1
	}
	if terminal.fileName == "" {
		return 0 // Rejected receipts retain the same View for an amended Intent.
	}
	defer terminal.clear()
	app.finishPresentedReceipt(ctx, store, client, terminal)
	return 0
}

func candidateBindingControlError(err error) error {
	switch {
	case errors.Is(err, errCandidateNotCaptured):
		return newControlError(codeArtifactInvalid,
			"Artifact candidate handle was not returned by capture in this Hook boundary; use view_handle for a View-offered Artifact")
	case errors.Is(err, errCandidateRepeated):
		return newControlError(codeInvalidArgument,
			"Intent repeats an Artifact candidate")
	default:
		return err
	}
}

func intentInputControlError(err error) *controlError {
	if errors.Is(err, agency.ErrLimit) {
		return newControlError(codeContentTooLarge,
			"Intent input exceeds a closed field or collection bound")
	}
	var validation *agency.ValidationError
	if errors.As(err, &validation) {
		switch intentValidationDiagnostic(validation) {
		case "required":
			return newControlError(codeInvalidArgument,
				"Intent input must include kind, payload, and consequence")
		case "duplicate_json":
			return newControlError(codeInvalidArgument,
				"Intent input contains a duplicate JSON field")
		case "noncanonical_field":
			return newControlError(codeInvalidArgument,
				"Intent input contains a non-canonical field")
		case "successor_noncanonical_field":
			return newControlError(codeInvalidArgument,
				"Intent successors may contain only self:true or one View-offered alias; keep kind and payload on the Intent")
		case "artifact_noncanonical_field":
			return newControlError(codeInvalidArgument,
				"Intent Artifacts may contain only kind and handle")
		case "closed_consequence":
			return newControlError(codeInvalidArgument,
				"Intent consequence must be copied exactly from the current View allowed_intents")
		case "root_shape":
			return newControlError(codeInvalidArgument,
				"handling.create requires at least one View-offered successor and no subject or Reference fields")
		case "subject_shape":
			return newControlError(codeInvalidArgument,
				"handling advance or resolve requires current.facts.handle as subject_handling and no Reference fields")
		case "reference_publish_shape":
			return newControlError(codeInvalidArgument,
				"reference.publish requires one new reference_key, exactly one Artifact, and no Handling fields or successors")
		case "reference_supersede_shape":
			return newControlError(codeInvalidArgument,
				"reference.supersede requires one View-offered reference_head, exactly one Artifact, and no Handling fields or successors")
		case "reference_retract_shape":
			return newControlError(codeInvalidArgument,
				"reference.retract requires one View-offered reference_head, no Artifact, and no Handling fields or successors")
		case "target_shape":
			return newControlError(codeInvalidArgument,
				"each successor must contain exactly one of self:true or one View-offered alias")
		case "artifact_kind":
			return newControlError(codeInvalidArgument,
				"each Artifact kind must be exactly candidate or view_handle")
		default:
			return newControlError(codeInvalidArgument,
				"Intent input has an invalid canonical field or structural shape")
		}
	}
	return newControlError(codeInvalidArgument,
		"Intent input must be exactly one JSON object without Markdown or trailing text")
}

func (app *terminal) finishPresentedReceipt(ctx context.Context, store *journalStore,
	client agencyClient, terminal clientJournal,
) {
	var presented clientJournal
	if err := store.withLock(false, func(directory *lockedJournalDirectory) error {
		var err error
		presented, err = directory.markPresented(terminal)
		return err
	}); err != nil {
		// The Receipt reached stdout, but the durable presentation transition
		// did not. The exact terminal operation remains replayable.
		presented.clear()
		return
	}
	defer presented.clear()
	if apiErr := client.End(ctx, presented.Attachment); apiErr != nil {
		// The accepted Receipt was already presented. The terminal journal
		// preserves an idempotent End retry without retaining the Intent.
		return
	}
	if err := store.withLock(false, func(directory *lockedJournalDirectory) error {
		return directory.remove(presented)
	}); err != nil {
		// End is durable. A later boundary or Hook end may idempotently replay
		// End and remove the presented terminal phase.
		return
	}
}

func readBoundedInput(reader io.Reader, maximum int, code controlErrorCode,
	message string,
) ([]byte, *controlError) {
	if reader == nil || maximum <= 0 {
		return nil, newControlError(codeInternal, "bounded stdin is unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if err != nil {
		clear(raw)
		return nil, newControlError(codeInvalidArgument, "stdin cannot be read")
	}
	if len(raw) > maximum {
		clear(raw)
		return nil, newControlError(code, message)
	}
	if len(raw) == 0 {
		return nil, newControlError(codeContentRequired, "stdin must not be empty")
	}
	return raw, nil
}

func agentReceiptOutcome(raw []byte) (string, error) {
	receipt, err := agency.ParseAgentReceiptProjectionCanonicalJSON(raw)
	if err != nil {
		return "", errors.New("Memory Agency returned an invalid AgentReceipt")
	}
	return receipt.Outcome().String(), nil
}

func (app *terminal) writeCanonicalProjection(raw []byte) int {
	if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return app.writeError(newControlError(codeInternal,
			"Memory Agency returned an invalid R7 projection"))
	}
	if _, err := app.stdout.Write(append(append([]byte(nil), raw...), '\n')); err != nil {
		return 1
	}
	return 0
}

func (app *terminal) writeJSON(value any) int {
	raw, err := marshalClosedJSON(value)
	if err != nil {
		return 1
	}
	return app.writeCanonicalProjection(raw)
}

func (app *terminal) writeCommandError(err error) int {
	var apiErr *controlError
	if errors.As(err, &apiErr) {
		return app.writeError(apiErr)
	}
	return app.writeError(clientStateError())
}

func (app *terminal) writeError(apiErr *controlError) int {
	if apiErr == nil {
		apiErr = newControlError(codeInternal, "internal R7 Agent terminal error")
	}
	if app.writeJSON(apiErr) != 0 {
		return 1
	}
	return apiErr.exitStatus()
}
