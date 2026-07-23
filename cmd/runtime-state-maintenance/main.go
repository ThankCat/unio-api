package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	platformredis "github.com/ThankCat/unio-gateway/internal/platform/redis"
	"github.com/ThankCat/unio-gateway/internal/platform/store"
)

const maximumRecoveryEvidenceBytes = 64 << 10

type commandKind string

const (
	commandBegin   commandKind = "begin"
	commandCommit  commandKind = "commit"
	commandRelease commandKind = "release"
)

type maintenanceCommand struct {
	kind         commandKind
	begin        runtimecontrol.BeginStateEpochRecoveryInput
	recoveryID   string
	revision     int64
	evidenceFile string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:], os.Stdout); err != nil {
		code := failure.CodeOf(err)
		if code == "" {
			code = failure.CodeBootstrapStoreFailed
		}
		_, _ = fmt.Fprintln(os.Stderr, code)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, output io.Writer) error {
	command, err := parseMaintenanceCommand(args)
	if err != nil {
		return failure.New(failure.CodeConfigInvalid, failure.WithMessage("invalid runtime state maintenance command"))
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	startupTimeout := cfg.Worker.StartupTimeout
	if startupTimeout <= 0 {
		startupTimeout = 30 * time.Second
	}
	startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	pool, err := store.OpenPostgres(startupCtx, cfg.DB)
	if err != nil {
		return err
	}
	defer pool.Close()
	redisClient, err := platformredis.OpenRedis(startupCtx, cfg.Redis)
	if err != nil {
		return err
	}
	defer redisClient.Close()
	breakerStore := breakerstore.NewStore(redisClient, cfg.Redis.KeyNamespace)
	if err := breakerStore.VerifySingleNodeDeployment(startupCtx); err != nil {
		return err
	}
	coordinator := runtimecontrol.NewStateEpochCoordinator(pool, breakerStore)

	var result runtimecontrol.StateEpochEnsureResult
	switch command.kind {
	case commandBegin:
		result, err = coordinator.BeginRecovery(ctx, command.begin)
	case commandCommit:
		var evidence []byte
		evidence, err = readRecoveryEvidence(command.evidenceFile)
		if err == nil {
			result, err = coordinator.CommitRecovery(ctx, runtimecontrol.CommitStateEpochRecoveryInput{
				RecoveryID:       command.recoveryID,
				Revision:         command.revision,
				RecoveryEvidence: evidence,
			})
		}
	case commandRelease:
		var evidence []byte
		evidence, err = readRecoveryEvidence(command.evidenceFile)
		if err == nil {
			result, err = coordinator.ReleaseRecovery(ctx, runtimecontrol.ReleaseStateEpochRecoveryInput{
				RecoveryID:      command.recoveryID,
				Revision:        command.revision,
				ReleaseEvidence: evidence,
			})
		}
	default:
		err = failure.New(failure.CodeConfigInvalid, failure.WithMessage("invalid runtime state maintenance command"))
	}
	if err != nil {
		return err
	}
	if result.State != runtimecontrol.StateEpochEnsureReady &&
		result.State != runtimecontrol.StateEpochEnsureAwaitingMaintenance &&
		result.State != runtimecontrol.StateEpochEnsureAwaitingRelease {
		return failure.New(failure.CodeBootstrapStoreFailed, failure.WithMessage("unexpected runtime state maintenance result"))
	}
	_, err = fmt.Fprintln(output, result.State)
	return err
}

func parseMaintenanceCommand(args []string) (maintenanceCommand, error) {
	if len(args) == 0 {
		return maintenanceCommand{}, errors.New("missing command")
	}
	switch args[0] {
	case string(commandBegin):
		return parseBeginCommand(args[1:])
	case string(commandCommit):
		return parseCommitCommand(args[1:])
	case string(commandRelease):
		return parseReleaseCommand(args[1:])
	default:
		return maintenanceCommand{}, errors.New("unknown command")
	}
}

func parseBeginCommand(args []string) (maintenanceCommand, error) {
	flags := flag.NewFlagSet(string(commandBegin), flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reasonValue := flags.String("reason", "", "state_loss or restore")
	detectedAtValue := flags.String("detected-at", "", "RFC3339 state-loss detection time")
	notBeforeValue := flags.String("not-before", "", "RFC3339 earliest commit time")
	operatorRef := flags.String("operator-ref", "", "non-secret operator or change reference")
	recoveryID := flags.String("recovery-id", "", "unique non-secret recovery identifier")
	expectedRevision := flags.Int64("expected-current-revision", 0, "current ready epoch revision")
	stateLossConfirmed := flags.Bool("confirm-state-loss", false, "confirm runtime state is untrusted")
	ingressBlocked := flags.Bool("confirm-external-ingress-blocked", false, "confirm external ingress is blocked")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return maintenanceCommand{}, errors.New("invalid begin flags")
	}
	if *reasonValue == "" || *detectedAtValue == "" || *notBeforeValue == "" || *operatorRef == "" ||
		strings.TrimSpace(*recoveryID) == "" || *expectedRevision < 1 ||
		!*stateLossConfirmed || !*ingressBlocked {
		return maintenanceCommand{}, errors.New("missing begin confirmation")
	}
	var reason runtimecontrol.StateEpochReason
	switch strings.TrimSpace(*reasonValue) {
	case string(runtimecontrol.StateEpochReasonStateLoss):
		reason = runtimecontrol.StateEpochReasonStateLoss
	case string(runtimecontrol.StateEpochReasonRestore):
		reason = runtimecontrol.StateEpochReasonRestore
	default:
		return maintenanceCommand{}, errors.New("invalid begin reason")
	}
	detectedAt, err := time.Parse(time.RFC3339, *detectedAtValue)
	if err != nil {
		return maintenanceCommand{}, errors.New("invalid detected-at")
	}
	notBefore, err := time.Parse(time.RFC3339, *notBeforeValue)
	if err != nil || notBefore.Before(detectedAt) {
		return maintenanceCommand{}, errors.New("invalid not-before")
	}
	if notBefore.After(detectedAt.Add(24 * time.Hour)) {
		return maintenanceCommand{}, errors.New("not-before exceeds recovery window")
	}
	return maintenanceCommand{
		kind: commandBegin,
		begin: runtimecontrol.BeginStateEpochRecoveryInput{
			RecoveryID:                      *recoveryID,
			ExpectedCurrentRevision:         *expectedRevision,
			Reason:                          reason,
			DetectedAt:                      detectedAt,
			NotBefore:                       notBefore,
			OperatorRef:                     *operatorRef,
			StateLossConfirmed:              *stateLossConfirmed,
			ExternalIngressBlockedConfirmed: *ingressBlocked,
		},
	}, nil
}

func parseCommitCommand(args []string) (maintenanceCommand, error) {
	flags := flag.NewFlagSet(string(commandCommit), flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	evidenceFile := flags.String("evidence-file", "", "approved recovery evidence JSON file")
	recoveryID := flags.String("recovery-id", "", "unique non-secret recovery identifier")
	revision := flags.Int64("revision", 0, "new ready epoch revision")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*evidenceFile) == "" ||
		strings.TrimSpace(*recoveryID) == "" || *revision < 2 {
		return maintenanceCommand{}, errors.New("invalid commit flags")
	}
	return maintenanceCommand{
		kind: commandCommit, recoveryID: *recoveryID, revision: *revision, evidenceFile: *evidenceFile,
	}, nil
}

func parseReleaseCommand(args []string) (maintenanceCommand, error) {
	flags := flag.NewFlagSet(string(commandRelease), flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	evidenceFile := flags.String("evidence-file", "", "post-commit Gateway smoke evidence JSON file")
	recoveryID := flags.String("recovery-id", "", "unique non-secret recovery identifier")
	revision := flags.Int64("revision", 0, "new ready epoch revision")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*evidenceFile) == "" ||
		strings.TrimSpace(*recoveryID) == "" || *revision < 2 {
		return maintenanceCommand{}, errors.New("invalid release flags")
	}
	return maintenanceCommand{
		kind: commandRelease, recoveryID: *recoveryID, revision: *revision, evidenceFile: *evidenceFile,
	}, nil
}

func readRecoveryEvidence(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("open recovery evidence"))
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximumRecoveryEvidenceBytes+1))
	if err != nil {
		return nil, failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("read recovery evidence"))
	}
	if len(raw) == 0 || len(raw) > maximumRecoveryEvidenceBytes {
		return nil, failure.New(failure.CodeConfigInvalid, failure.WithMessage("recovery evidence size is invalid"))
	}
	return raw, nil
}
