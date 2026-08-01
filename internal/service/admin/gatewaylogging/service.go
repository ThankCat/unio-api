package gatewaylogging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

const gatewayStatusTimeout = 2 * time.Second

type ControlStore interface {
	GatewayLoggingControl(context.Context) (appsettings.GatewayLoggingControlSnapshot, error)
	StartGatewayDebugSession(context.Context, time.Duration, string, int64, time.Time) (appsettings.GatewayLoggingControlSnapshot, error)
	StopGatewayDebugSession(context.Context, int64, time.Time) (appsettings.GatewayLoggingControlSnapshot, error)
}

type Service struct {
	control     ControlStore
	client      *http.Client
	internalURL []string
	token       string
	lokiURL     string
	now         func() time.Time
}

type Snapshot struct {
	Mode      string           `json:"mode"`
	Control   ControlStatus    `json:"control"`
	Instances []InstanceStatus `json:"instances"`
}

type ControlStatus struct {
	Active          bool       `json:"active"`
	SessionID       string     `json:"session_id,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	EnabledByUserID int64      `json:"enabled_by_user_id"`
	Revision        int64      `json:"revision"`
}

type InstanceStatus struct {
	URL             string     `json:"url"`
	State           string     `json:"state"`
	Error           string     `json:"error,omitempty"`
	InstanceID      string     `json:"instance_id,omitempty"`
	Environment     string     `json:"environment,omitempty"`
	BaselineLevel   string     `json:"baseline_level,omitempty"`
	EffectiveLevel  string     `json:"effective_level,omitempty"`
	DebugSessionID  string     `json:"debug_session_id,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	AppliedRevision int64      `json:"applied_revision,omitempty"`
}

func NewService(control ControlStore, client *http.Client, internalURLs []string, token, lokiURL string) *Service {
	if client == nil {
		client = http.DefaultClient
	}
	return &Service{
		control:     control,
		client:      client,
		internalURL: append([]string(nil), internalURLs...),
		token:       token,
		lokiURL:     strings.TrimRight(strings.TrimSpace(lokiURL), "/"),
		now:         time.Now,
	}
}

func (s *Service) Get(ctx context.Context) (Snapshot, error) {
	if s == nil || s.control == nil {
		return Snapshot{}, errors.New("gateway logging control is unavailable")
	}
	control, err := s.control.GatewayLoggingControl(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return s.snapshot(ctx, control), nil
}

func (s *Service) Start(ctx context.Context, durationMinutes int, reason string, enabledByUserID int64) (Snapshot, error) {
	if s == nil || s.control == nil {
		return Snapshot{}, errors.New("gateway logging control is unavailable")
	}
	current, err := s.Get(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	for _, instance := range current.Instances {
		if instance.State == "environment_debug" {
			return Snapshot{}, fmt.Errorf(
				"%w: temporary DEBUG is unavailable while a Gateway instance uses an environment DEBUG baseline",
				appsettings.ErrGatewayDebugRequestInvalid,
			)
		}
	}
	control, err := s.control.StartGatewayDebugSession(
		ctx, time.Duration(durationMinutes)*time.Minute, reason, enabledByUserID, s.now(),
	)
	if err != nil {
		return Snapshot{}, err
	}
	return s.snapshot(ctx, control), nil
}

func (s *Service) Stop(ctx context.Context, changedByUserID int64) (Snapshot, error) {
	if s == nil || s.control == nil {
		return Snapshot{}, errors.New("gateway logging control is unavailable")
	}
	control, err := s.control.StopGatewayDebugSession(ctx, changedByUserID, s.now())
	if err != nil {
		return Snapshot{}, err
	}
	return s.snapshot(ctx, control), nil
}

func (s *Service) snapshot(ctx context.Context, control appsettings.GatewayLoggingControlSnapshot) Snapshot {
	now := s.now()
	active := control.Setting.SessionID != "" && control.Setting.ExpiresAt.After(now)
	status := ControlStatus{
		Active:          active,
		SessionID:       control.Setting.SessionID,
		Reason:          control.Setting.Reason,
		EnabledByUserID: control.Setting.EnabledByUserID,
		Revision:        control.Setting.Revision,
	}
	if active {
		startedAt := control.Setting.StartedAt
		expiresAt := control.Setting.ExpiresAt
		status.StartedAt = &startedAt
		status.ExpiresAt = &expiresAt
	}
	instances := s.fetchInstances(ctx, status)
	mode := "info"
	for _, instance := range instances {
		if instance.State == "environment_debug" {
			mode = "environment_debug"
			break
		}
	}
	if mode != "environment_debug" && active {
		mode = "debug"
	}
	return Snapshot{
		Mode:      mode,
		Control:   status,
		Instances: instances,
	}
}

func (s *Service) fetchInstances(ctx context.Context, control ControlStatus) []InstanceStatus {
	statuses := make([]InstanceStatus, len(s.internalURL))
	var wg sync.WaitGroup
	for index, baseURL := range s.internalURL {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[index] = s.fetchInstance(ctx, baseURL, control)
		}()
	}
	wg.Wait()
	return statuses
}

func (s *Service) fetchInstance(ctx context.Context, baseURL string, control ControlStatus) InstanceStatus {
	status := InstanceStatus{URL: strings.TrimRight(baseURL, "/"), State: "unreachable"}
	requestCtx, cancel := context.WithTimeout(ctx, gatewayStatusTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, status.URL+"/internal/v1/logging/status", nil)
	if err != nil {
		status.Error = "invalid gateway internal URL"
		return status
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		status.Error = fmt.Sprintf("gateway status returned HTTP %d", resp.StatusCode)
		return status
	}
	var runtimeStatus logging.GatewayStatus
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&runtimeStatus); err != nil {
		status.Error = "gateway status response is invalid"
		return status
	}
	status.InstanceID = runtimeStatus.InstanceID
	status.Environment = runtimeStatus.Environment
	status.BaselineLevel = runtimeStatus.BaselineLevel
	status.EffectiveLevel = runtimeStatus.EffectiveLevel
	status.DebugSessionID = runtimeStatus.DebugSessionID
	status.ExpiresAt = runtimeStatus.ExpiresAt
	status.AppliedRevision = runtimeStatus.AppliedRevision
	status.State = instanceApplicationState(control, runtimeStatus)
	return status
}

func instanceApplicationState(control ControlStatus, instance logging.GatewayStatus) string {
	if instance.BaselineLevel == "debug" {
		return "environment_debug"
	}
	if control.Active {
		if instance.EffectiveLevel == "debug" && instance.DebugSessionID == control.SessionID &&
			instance.AppliedRevision >= control.Revision {
			return "applied"
		}
		return "pending"
	}
	if instance.EffectiveLevel == instance.BaselineLevel && instance.AppliedRevision >= control.Revision {
		return "applied"
	}
	return "pending"
}
