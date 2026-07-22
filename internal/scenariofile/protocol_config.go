package scenariofile

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// TriggersToProtocol converts validated scenario-file trigger models to their
// protocol-owned wire DTOs. It preserves nil versus explicitly empty slices so
// encoding/json emits the same bytes as the historical config-backed shape.
func TriggersToProtocol(triggers []config.TriggerConfig) ([]protocol.TriggerConfig, error) {
	if triggers == nil {
		return nil, nil
	}

	out := make([]protocol.TriggerConfig, len(triggers))
	for i := range triggers {
		converted, err := triggerToProtocol(triggers[i])
		if err != nil {
			return nil, fmt.Errorf("trigger %q: %w", triggers[i].Name, err)
		}

		out[i] = converted
	}

	return out, nil
}

//nolint:dupl // config↔protocol mappings stay explicit so field drift is reviewable.
func triggerToProtocol(trigger config.TriggerConfig) (protocol.TriggerConfig, error) {
	action, err := actionToProtocol(trigger.Action)
	if err != nil {
		return protocol.TriggerConfig{}, err
	}

	return protocol.TriggerConfig{
		Name:       trigger.Name,
		Enabled:    cloneBool(trigger.Enabled),
		Schedule:   scheduleToProtocol(trigger.Schedule),
		Watch:      watchToProtocol(trigger.Watch),
		GCX:        gcxToProtocol(trigger.GCX),
		Completion: completionToProtocol(trigger.Completion),
		Action:     action,
		Policy: protocol.TriggerPolicy{
			CatchUp: trigger.Policy.CatchUp, Overlap: trigger.Policy.Overlap, RateLimit: trigger.Policy.RateLimit,
		},
	}, nil
}

func scheduleToProtocol(schedule *config.ScheduleConfig) *protocol.ScheduleConfig {
	if schedule == nil {
		return nil
	}

	return &protocol.ScheduleConfig{Cron: schedule.Cron, Every: schedule.Every, Timezone: schedule.Timezone}
}

func watchToProtocol(watch *config.WatchConfig) *protocol.WatchConfig {
	if watch == nil {
		return nil
	}

	return &protocol.WatchConfig{
		Repo: watch.Repo, Role: watch.Role, Paths: cloneStrings(watch.Paths),
		Ignore: cloneStrings(watch.Ignore), Debounce: watch.Debounce,
	}
}

func gcxToProtocol(gcx *config.GCXConfig) *protocol.GCXConfig {
	if gcx == nil {
		return nil
	}

	return &protocol.GCXConfig{
		Event: gcx.Event, Context: gcx.Context, Every: gcx.Every, Timeout: gcx.Timeout,
		OnCallUserID: gcx.OnCallUserID, ScheduleIDs: cloneStrings(gcx.ScheduleIDs),
		TeamIDs: cloneStrings(gcx.TeamIDs), IntegrationIDs: cloneStrings(gcx.IntegrationIDs),
		States: cloneStrings(gcx.States), MaxAge: gcx.MaxAge, Limit: gcx.Limit,
	}
}

func completionToProtocol(completion *config.CompletionConfig) *protocol.CompletionConfig {
	if completion == nil {
		return nil
	}

	return &protocol.CompletionConfig{Event: completion.Event, Session: completion.Session}
}

func actionToProtocol(action config.ActionConfig) (protocol.ActionConfig, error) {
	autoCleanup, err := json.Marshal(action.AutoCleanup)
	if err != nil {
		return protocol.ActionConfig{}, fmt.Errorf("marshal action.auto_cleanup: %w", err)
	}

	return protocol.ActionConfig{
		Type: action.Type, Command: action.Command, Repo: action.Repo, Timeout: action.Timeout,
		Mutating: action.Mutating, Sandbox: cloneBool(action.Sandbox),
		SandboxConfig: sandboxToProtocol(action.SandboxConfig),
		Prompt:        action.Prompt, Agent: action.Agent, Model: action.Model, Ensure: action.Ensure,
		AutoCleanup: autoCleanup, IdleTimeout: action.IdleTimeout, Scenario: action.Scenario,
		Tracker: trackerToProtocol(action.Tracker), Body: action.Body,
		NotifyOnComplete: action.NotifyOnComplete, NotifyMessage: action.NotifyMessage,
		NotifyPriority: action.NotifyPriority,
		Deliver: protocol.DeliverConfig{
			Inbox: action.Deliver.Inbox, Topic: action.Deliver.Topic, Store: action.Deliver.Store,
			Wake: action.Deliver.Wake, Required: action.Deliver.Required,
		},
	}, nil
}

func trackerToProtocol(tracker *config.TrackerConfig) *protocol.TrackerConfig {
	if tracker == nil {
		return nil
	}

	return &protocol.TrackerConfig{
		Provider: tracker.Provider, Repo: tracker.Repo, ActiveState: tracker.ActiveState,
		ActiveLabels: cloneStrings(tracker.ActiveLabels), Assignee: tracker.Assignee,
		Grace: tracker.Grace, MaxConcurrent: tracker.MaxConcurrent, Reap: tracker.Reap, Limit: tracker.Limit,
	}
}

//nolint:dupl // config↔protocol mappings stay explicit so field drift is reviewable.
func sandboxToProtocol(sandbox *config.SandboxConfig) *protocol.SandboxConfig {
	if sandbox == nil {
		return nil
	}

	return &protocol.SandboxConfig{
		Enabled: sandbox.Enabled, Disabled: cloneBool(sandbox.Disabled), Backend: sandbox.Backend,
		Command: sandbox.Command, Profile: sandbox.Profile, Features: cloneStrings(sandbox.Features),
		ReadDirs: cloneStrings(sandbox.ReadDirs), WriteDirs: cloneStrings(sandbox.WriteDirs),
		ReadFiles: cloneStrings(sandbox.ReadFiles), WriteFiles: cloneStrings(sandbox.WriteFiles),
		SignalMode: sandbox.SignalMode, Network: sandboxNetworkToProtocol(sandbox.Network),
	}
}

func sandboxNetworkToProtocol(network *config.SandboxNetworkConfig) *protocol.SandboxNetworkConfig {
	if network == nil {
		return nil
	}

	return &protocol.SandboxNetworkConfig{
		Block: network.Block, AllowDomains: cloneStrings(network.AllowDomains),
	}
}

// TriggersFromProtocol converts scenario-start wire DTOs to config/domain
// models before validation and runtime use in the daemon.
func TriggersFromProtocol(triggers []protocol.TriggerConfig) ([]config.TriggerConfig, error) {
	if triggers == nil {
		return nil, nil
	}

	out := make([]config.TriggerConfig, len(triggers))
	for i := range triggers {
		converted, err := triggerFromProtocol(triggers[i])
		if err != nil {
			return nil, fmt.Errorf("trigger %q: %w", triggers[i].Name, err)
		}

		out[i] = converted
	}

	return out, nil
}

//nolint:dupl // config↔protocol mappings stay explicit so field drift is reviewable.
func triggerFromProtocol(trigger protocol.TriggerConfig) (config.TriggerConfig, error) {
	action, err := actionFromProtocol(trigger.Action)
	if err != nil {
		return config.TriggerConfig{}, err
	}

	return config.TriggerConfig{
		Name:       trigger.Name,
		Enabled:    cloneBool(trigger.Enabled),
		Schedule:   scheduleFromProtocol(trigger.Schedule),
		Watch:      watchFromProtocol(trigger.Watch),
		GCX:        gcxFromProtocol(trigger.GCX),
		Completion: completionFromProtocol(trigger.Completion),
		Action:     action,
		Policy: config.TriggerPolicy{
			CatchUp: trigger.Policy.CatchUp, Overlap: trigger.Policy.Overlap, RateLimit: trigger.Policy.RateLimit,
		},
	}, nil
}

func scheduleFromProtocol(schedule *protocol.ScheduleConfig) *config.ScheduleConfig {
	if schedule == nil {
		return nil
	}

	return &config.ScheduleConfig{Cron: schedule.Cron, Every: schedule.Every, Timezone: schedule.Timezone}
}

func watchFromProtocol(watch *protocol.WatchConfig) *config.WatchConfig {
	if watch == nil {
		return nil
	}

	return &config.WatchConfig{
		Repo: watch.Repo, Role: watch.Role, Paths: cloneStrings(watch.Paths),
		Ignore: cloneStrings(watch.Ignore), Debounce: watch.Debounce,
	}
}

func gcxFromProtocol(gcx *protocol.GCXConfig) *config.GCXConfig {
	if gcx == nil {
		return nil
	}

	return &config.GCXConfig{
		Event: gcx.Event, Context: gcx.Context, Every: gcx.Every, Timeout: gcx.Timeout,
		OnCallUserID: gcx.OnCallUserID, ScheduleIDs: cloneStrings(gcx.ScheduleIDs),
		TeamIDs: cloneStrings(gcx.TeamIDs), IntegrationIDs: cloneStrings(gcx.IntegrationIDs),
		States: cloneStrings(gcx.States), MaxAge: gcx.MaxAge, Limit: gcx.Limit,
	}
}

func completionFromProtocol(completion *protocol.CompletionConfig) *config.CompletionConfig {
	if completion == nil {
		return nil
	}

	return &config.CompletionConfig{Event: completion.Event, Session: completion.Session}
}

func actionFromProtocol(action protocol.ActionConfig) (config.ActionConfig, error) {
	var autoCleanup any
	if len(action.AutoCleanup) > 0 && !bytes.Equal(bytes.TrimSpace(action.AutoCleanup), []byte("null")) {
		if err := json.Unmarshal(action.AutoCleanup, &autoCleanup); err != nil {
			return config.ActionConfig{}, fmt.Errorf("decode action.auto_cleanup: %w", err)
		}
	}

	return config.ActionConfig{
		Type: action.Type, Command: action.Command, Repo: action.Repo, Timeout: action.Timeout,
		Mutating: action.Mutating, Sandbox: cloneBool(action.Sandbox),
		SandboxConfig: sandboxFromProtocol(action.SandboxConfig),
		Prompt:        action.Prompt, Agent: action.Agent, Model: action.Model, Ensure: action.Ensure,
		AutoCleanup: autoCleanup, IdleTimeout: action.IdleTimeout, Scenario: action.Scenario,
		Tracker: trackerFromProtocol(action.Tracker), Body: action.Body,
		NotifyOnComplete: action.NotifyOnComplete, NotifyMessage: action.NotifyMessage,
		NotifyPriority: action.NotifyPriority,
		Deliver: config.DeliverConfig{
			Inbox: action.Deliver.Inbox, Topic: action.Deliver.Topic, Store: action.Deliver.Store,
			Wake: action.Deliver.Wake, Required: action.Deliver.Required,
		},
	}, nil
}

func trackerFromProtocol(tracker *protocol.TrackerConfig) *config.TrackerConfig {
	if tracker == nil {
		return nil
	}

	return &config.TrackerConfig{
		Provider: tracker.Provider, Repo: tracker.Repo, ActiveState: tracker.ActiveState,
		ActiveLabels: cloneStrings(tracker.ActiveLabels), Assignee: tracker.Assignee,
		Grace: tracker.Grace, MaxConcurrent: tracker.MaxConcurrent, Reap: tracker.Reap, Limit: tracker.Limit,
	}
}

//nolint:dupl // config↔protocol mappings stay explicit so field drift is reviewable.
func sandboxFromProtocol(sandbox *protocol.SandboxConfig) *config.SandboxConfig {
	if sandbox == nil {
		return nil
	}

	return &config.SandboxConfig{
		Enabled: sandbox.Enabled, Disabled: cloneBool(sandbox.Disabled), Backend: sandbox.Backend,
		Command: sandbox.Command, Profile: sandbox.Profile, Features: cloneStrings(sandbox.Features),
		ReadDirs: cloneStrings(sandbox.ReadDirs), WriteDirs: cloneStrings(sandbox.WriteDirs),
		ReadFiles: cloneStrings(sandbox.ReadFiles), WriteFiles: cloneStrings(sandbox.WriteFiles),
		SignalMode: sandbox.SignalMode, Network: sandboxNetworkFromProtocol(sandbox.Network),
	}
}

func sandboxNetworkFromProtocol(network *protocol.SandboxNetworkConfig) *config.SandboxNetworkConfig {
	if network == nil {
		return nil
	}

	return &config.SandboxNetworkConfig{
		Block: network.Block, AllowDomains: cloneStrings(network.AllowDomains),
	}
}

// LifecycleToProtocol maps scenario lifecycle config to its wire DTO.
func LifecycleToProtocol(lifecycle config.ScenarioLifecycleConfig) protocol.ScenarioLifecycleConfig {
	return protocol.ScenarioLifecycleConfig{Cleanup: lifecycle.Cleanup, Delay: lifecycle.Delay}
}

// LifecycleFromProtocol maps a wire lifecycle DTO to the domain model that
// owns cleanup defaults, duration parsing, and validation.
func LifecycleFromProtocol(lifecycle protocol.ScenarioLifecycleConfig) config.ScenarioLifecycleConfig {
	return config.ScenarioLifecycleConfig{Cleanup: lifecycle.Cleanup, Delay: lifecycle.Delay}
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}

	clone := *value

	return &clone
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}

	return append([]string{}, values...)
}
