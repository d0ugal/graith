package cigate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/cipolicy"
)

const artifactUploadSkew = 5 * time.Minute

func Evaluate(input EvaluationInput, anchors TrustAnchors, store ReplayStore) (Evaluation, error) {
	return EvaluateAt(input, anchors, store, time.Now().UTC())
}

func EvaluateAt(input EvaluationInput, anchors TrustAnchors, store ReplayStore, now time.Time) (Evaluation, error) {
	evaluation := failureEvaluation(input, now, "validation has not completed")

	report, retained, err := validateEvaluation(input, anchors, store, now)
	if err != nil {
		evaluation.Reasons = []string{err.Error()}
		evaluation.Report = report
		evaluation.Evidence = retained
		evaluation.Check.Summary = err.Error()

		return evaluation, err
	}

	evaluation.SchemaVersion = SchemaVersion
	evaluation.Report = report
	evaluation.Evidence = retained
	evaluation.Reasons = nil
	evaluation.Check = CheckRun{
		Name:        CheckName,
		HeadSHA:     input.Event.IntendedSHA,
		Status:      "completed",
		Conclusion:  "success",
		Title:       "CI evidence accepted",
		Summary:     fmt.Sprintf("Accepted %d result row(s) for policy %s.", len(report.Accepted), anchors.Policy.PolicyDigest),
		CompletedAt: now.UTC(),
	}

	return evaluation, nil
}

func validateEvaluation(input EvaluationInput, anchors TrustAnchors, store ReplayStore, now time.Time) (cipolicy.FanInReport, RetainedEvidence, error) {
	retained, retainedErr := retainedEvidence(input, anchors)
	if retainedErr != nil {
		return cipolicy.FanInReport{}, RetainedEvidence{}, retainedErr
	}

	if now.IsZero() {
		return cipolicy.FanInReport{}, retained, errors.New("evaluation clock is required")
	}

	if input.SchemaVersion != SchemaVersion {
		return cipolicy.FanInReport{}, retained, fmt.Errorf("unsupported evaluation schema version %d", input.SchemaVersion)
	}

	if !input.Now.IsZero() {
		return cipolicy.FanInReport{}, retained, errors.New("evaluation input cannot supply trusted time; use the evaluator clock")
	}

	if store == nil {
		return cipolicy.FanInReport{}, retained, errors.New("replay store is required")
	}

	if err := validateTrustAnchors(anchors, now); err != nil {
		return cipolicy.FanInReport{}, retained, err
	}

	if err := validateTrustedEchoes(input, anchors, now); err != nil {
		return cipolicy.FanInReport{}, retained, err
	}

	if err := validateDelivery(anchors.Config.App, input.Delivery, input.Event); err != nil {
		return cipolicy.FanInReport{}, retained, err
	}

	if err := validateEvent(anchors.Config, input.Event); err != nil {
		return cipolicy.FanInReport{}, retained, err
	}

	if err := validatePlanBinding(anchors.Policy, input.Plan, input.Event, now); err != nil {
		return cipolicy.FanInReport{}, retained, err
	}

	if err := validateTrustBoundary(input.Event, input.Plan); err != nil {
		return cipolicy.FanInReport{}, retained, err
	}

	if err := validateEvidence(anchors.Policy, input.Plan, input.Results, input.Evidence, input.Event, now); err != nil {
		return cipolicy.FanInReport{}, retained, err
	}

	report, err := cipolicy.FanIn(anchors.Policy, input.Plan, input.Results, now)
	if err != nil {
		return report, retained, err
	}

	if err := store.Reserve(ReplayKey{Kind: "delivery", Value: input.Delivery.ID}); err != nil {
		return report, retained, err
	}

	if err := store.Reserve(ReplayKey{Kind: "bundle", Value: retained.BundleDigest}); err != nil {
		return report, retained, err
	}

	return report, retained, nil
}

func validateTrustAnchors(anchors TrustAnchors, now time.Time) error {
	if err := anchors.Config.Validate(); err != nil {
		return fmt.Errorf("trusted config: %w", err)
	}

	if err := validateDigest("expected policy", anchors.ExpectedPolicyDigest); err != nil {
		return fmt.Errorf("trusted policy: %w", err)
	}

	if err := anchors.Policy.ValidateAt(now); err != nil {
		return fmt.Errorf("trusted policy: %w", err)
	}

	if anchors.Policy.PolicyDigest != anchors.ExpectedPolicyDigest {
		return fmt.Errorf("trusted policy digest %s does not match expected policy digest %s", anchors.Policy.PolicyDigest, anchors.ExpectedPolicyDigest)
	}

	if err := validateEvaluator(anchors.Config.Deployment, anchors.Evaluator); err != nil {
		return fmt.Errorf("trusted evaluator: %w", err)
	}

	return nil
}

func validateTrustedEchoes(input EvaluationInput, anchors TrustAnchors, now time.Time) error {
	if configEchoPresent(input.Config) {
		if err := input.Config.Validate(); err != nil {
			return fmt.Errorf("bundle config echo is invalid: %w", err)
		}

		if !canonicalEqual(input.Config, anchors.Config) {
			return errors.New("bundle config does not match trusted evaluator config")
		}
	}

	if policyEchoPresent(input.Policy) {
		if err := input.Policy.ValidateAt(now); err != nil {
			return fmt.Errorf("bundle policy echo is invalid: %w", err)
		}

		if input.Policy.PolicyDigest != anchors.Policy.PolicyDigest {
			return fmt.Errorf("bundle policy digest %s does not match trusted policy digest %s", input.Policy.PolicyDigest, anchors.Policy.PolicyDigest)
		}
	}

	if evaluatorEchoPresent(input.Evaluator) {
		if input.Evaluator != anchors.Evaluator {
			return errors.New("bundle evaluator identity does not match trusted evaluator identity")
		}
	}

	return nil
}

func configEchoPresent(config Config) bool {
	return !canonicalEqual(config, Config{})
}

func policyEchoPresent(policy cipolicy.Manifest) bool {
	return !canonicalEqual(policy, cipolicy.Manifest{})
}

func evaluatorEchoPresent(evaluator EvaluatorIdentity) bool {
	return evaluator != EvaluatorIdentity{}
}

func canonicalEqual(left, right any) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)

	return leftErr == nil && rightErr == nil && string(leftData) == string(rightData)
}

func validateEvaluator(deployment DeploymentContract, evaluator EvaluatorIdentity) error {
	if evaluator.Name != CheckName {
		return fmt.Errorf("evaluator name %q must be %q", evaluator.Name, CheckName)
	}

	if strings.TrimSpace(evaluator.Version) == "" {
		return errors.New("evaluator version is required")
	}

	if evaluator.ReleaseDigest != deployment.ReleaseDigest {
		return fmt.Errorf("evaluator release digest %s does not match deployment release digest %s", evaluator.ReleaseDigest, deployment.ReleaseDigest)
	}

	if evaluator.SourceDigest != deployment.EvaluatorDigest {
		return fmt.Errorf("evaluator source digest %s does not match deployment evaluator digest %s", evaluator.SourceDigest, deployment.EvaluatorDigest)
	}

	return nil
}

func validateDelivery(app AppContract, delivery DeliveryContext, event EventContext) error {
	if strings.TrimSpace(delivery.ID) == "" {
		return errors.New("event delivery id is required")
	}

	if delivery.Event != event.GitHubEvent {
		return fmt.Errorf("delivery event %s does not match event context %s", delivery.Event, event.GitHubEvent)
	}

	if !delivery.SignatureValidated {
		return errors.New("webhook signature validation is required")
	}

	if err := validateDigest("webhook body", delivery.BodyDigest); err != nil {
		return err
	}

	if !contains(app.Events, delivery.Event) {
		return fmt.Errorf("delivery event %s is not enabled for the App", delivery.Event)
	}

	return nil
}

func validateEvent(config Config, event EventContext) error {
	if event.Source != GitHubWebhookSource {
		return fmt.Errorf("event source %q is untrusted", event.Source)
	}

	if !contains([]string{"pull_request", "merge_group"}, event.GitHubEvent) {
		return fmt.Errorf("GitHub event %q is unsupported by the gate", event.GitHubEvent)
	}

	if event.GitHubEvent == "merge_group" && event.Action != "checks_requested" {
		return fmt.Errorf("merge_group action %q is unsupported", event.Action)
	}

	if event.GitHubEvent == "pull_request" {
		if !contains([]string{"opened", "ready_for_review", "reopened", "synchronize"}, event.Action) {
			return fmt.Errorf("pull_request action %q is unsupported", event.Action)
		}

		if event.PolicyGitHubEvent != "" && event.PolicyGitHubEvent != "pull_request" {
			return fmt.Errorf("pull_request policy event %q is unsupported", event.PolicyGitHubEvent)
		}
	}

	if event.Repository != config.Repository || event.BaseRepository != config.Repository {
		return fmt.Errorf("event repository %s/%s does not match config repository %s", event.Repository, event.BaseRepository, config.Repository)
	}

	if event.PolicyDigest == "" {
		return errors.New("event policy digest is required")
	}

	for label, digest := range map[string]string{
		"intended": event.IntendedSHA,
		"head":     event.HeadSHA,
		"base":     event.BaseSHA,
	} {
		if !gitDigestPattern.MatchString(digest) {
			return fmt.Errorf("%s SHA %q is not a git digest", label, digest)
		}
	}

	if event.IntendedSHA != event.HeadSHA {
		return fmt.Errorf("intended SHA %s does not match trusted head SHA %s", event.IntendedSHA, event.HeadSHA)
	}

	if strings.TrimSpace(event.Ref) == "" || strings.TrimSpace(event.BaseRef) == "" {
		return errors.New("event ref and base ref are required")
	}

	if event.HeadRepository == "" {
		return errors.New("event head repository is required")
	}

	return nil
}

func validatePlanBinding(policy cipolicy.Manifest, plan cipolicy.RunPlan, event EventContext, now time.Time) error {
	if err := policy.ValidateAt(now); err != nil {
		return err
	}

	if len(plan.Jobs) == 0 {
		return errors.New("zero-job plan cannot satisfy the gate")
	}

	if err := plan.ValidateAt(policy, now); err != nil {
		return err
	}

	if policy.PolicyDigest != event.PolicyDigest {
		return fmt.Errorf("event policy digest %s does not match trusted policy digest %s", event.PolicyDigest, policy.PolicyDigest)
	}

	if plan.PolicyDigest != policy.PolicyDigest {
		return fmt.Errorf("plan policy digest %s does not match trusted policy digest %s", plan.PolicyDigest, policy.PolicyDigest)
	}

	if plan.Source.Commit != event.IntendedSHA {
		return fmt.Errorf("plan source commit %s does not match intended SHA %s", plan.Source.Commit, event.IntendedSHA)
	}

	if plan.Source.BaseRef != event.BaseRef || plan.Source.HeadRef != event.HeadRef || plan.Source.Ref != event.Ref ||
		plan.Source.HeadRepository != event.HeadRepository {
		return errors.New("plan source revision does not match trusted event refs")
	}

	policyEvent := event.PolicyGitHubEvent
	if policyEvent == "" {
		policyEvent = event.GitHubEvent
	}

	if plan.Event.GitHubEvent != policyEvent {
		return fmt.Errorf("plan GitHub event %s does not match trusted policy event %s", plan.Event.GitHubEvent, policyEvent)
	}

	if plan.TrustTier != event.TrustTier {
		return fmt.Errorf("plan trust tier %s does not match trusted event tier %s", plan.TrustTier, event.TrustTier)
	}

	return nil
}

func validateTrustBoundary(event EventContext, plan cipolicy.RunPlan) error {
	if event.GitHubEvent == "merge_group" {
		if event.PolicyGitHubEvent != "pull_request" || plan.Event.Event != "pull-request" || plan.TrustTier != "trusted-base" {
			return errors.New("merge_group must be evaluated through the existing P2 pull_request trusted-base event")
		}

		if event.PullRequestFork || event.SameRepositoryAgent || !event.TrustedBase {
			return errors.New("merge_group requires exactly the trusted-base policy context")
		}

		if plan.Event.PullRequestFork || plan.Event.SameRepositoryAgent || !plan.Event.TrustedBase {
			return errors.New("merge_group plan trust context must be trusted-base")
		}

		return nil
	}

	contexts := 0
	if event.PullRequestFork {
		contexts++
	}

	if event.SameRepositoryAgent {
		contexts++
	}

	if event.TrustedBase {
		contexts++
	}

	if contexts != 1 {
		return errors.New("pull_request event requires exactly one trusted event context")
	}

	forkRepository := event.HeadRepository != event.BaseRepository
	switch event.TrustTier {
	case "fork-untrusted":
		if !event.PullRequestFork || !forkRepository {
			return errors.New("fork-untrusted tier requires a fork head repository")
		}
	case "same-repository-agent":
		if !event.SameRepositoryAgent || forkRepository {
			return errors.New("same-repository-agent tier requires a same-repository head")
		}
	case "trusted-base":
		if !event.TrustedBase || forkRepository {
			return errors.New("trusted-base tier requires a trusted same-repository base evaluation")
		}
	default:
		return fmt.Errorf("pull_request trust tier %q is unsupported", event.TrustTier)
	}

	if plan.Event.PullRequestFork != event.PullRequestFork ||
		plan.Event.SameRepositoryAgent != event.SameRepositoryAgent ||
		plan.Event.TrustedBase != event.TrustedBase {
		return errors.New("plan trust context does not match trusted event context")
	}

	return nil
}

func validateEvidence(policy cipolicy.Manifest, plan cipolicy.RunPlan, results []cipolicy.ResultRecord, evidence []CoordinateEvidence, event EventContext, now time.Time) error {
	if len(plan.Jobs) == 0 {
		return errors.New("zero-job plan cannot satisfy the gate")
	}

	if len(results) == 0 {
		return errors.New("gate evidence requires at least one result")
	}

	if len(evidence) == 0 {
		return errors.New("gate evidence requires coordinate provenance")
	}

	traces, err := workflowTraces(policy, plan)
	if err != nil {
		return err
	}

	byCoordinate := map[string]CoordinateEvidence{}

	for _, item := range evidence {
		key := coordinateKey(item.Mode, item.Coordinate)
		if _, exists := byCoordinate[key]; exists {
			return fmt.Errorf("duplicate coordinate evidence for %s/%s", item.Mode, item.Coordinate)
		}

		byCoordinate[key] = item
	}

	for _, result := range results {
		if err := cipolicy.ValidateResultRecord(policy, plan, result, now); err != nil {
			return err
		}

		key := coordinateKey(result.Mode, result.Coordinate)

		item, ok := byCoordinate[key]
		if !ok {
			return fmt.Errorf("missing coordinate evidence for %s/%s", result.Mode, result.Coordinate)
		}

		trace := traces[key]
		if trace.WorkflowPath == "" || trace.WorkflowSHA256 == "" {
			return fmt.Errorf("manifest trace for %s/%s lacks workflow identity", result.Mode, result.Coordinate)
		}

		if err := validateCoordinateEvidence(plan, result, item, trace, event, now); err != nil {
			return err
		}

		delete(byCoordinate, key)
	}

	if len(byCoordinate) > 0 {
		keys := make([]string, 0, len(byCoordinate))
		for key := range byCoordinate {
			keys = append(keys, key)
		}

		sort.Strings(keys)

		return fmt.Errorf("extra coordinate evidence: %s", strings.Join(keys, ", "))
	}

	return nil
}

func validateCoordinateEvidence(plan cipolicy.RunPlan, result cipolicy.ResultRecord, item CoordinateEvidence, trace cipolicy.LegacyTrace, event EventContext, now time.Time) error {
	if item.Run.ID <= 0 || item.Run.Attempt <= 0 {
		return fmt.Errorf("run provenance for %s/%s requires a positive run id and attempt", result.Mode, result.Coordinate)
	}

	if item.Run.Repository != event.Repository || item.Artifact.Repository != event.Repository {
		return fmt.Errorf("provenance repository for %s/%s does not match trusted event repository", result.Mode, result.Coordinate)
	}

	if item.Run.Event != event.GitHubEvent {
		return fmt.Errorf("run event %s for %s/%s does not match trusted event %s", item.Run.Event, result.Mode, result.Coordinate, event.GitHubEvent)
	}

	if item.Run.WorkflowPath != trace.WorkflowPath || item.Artifact.WorkflowPath != trace.WorkflowPath {
		return fmt.Errorf("workflow path for %s/%s does not match trusted manifest path %s", result.Mode, result.Coordinate, trace.WorkflowPath)
	}

	if item.Run.WorkflowBlobSHA != trace.WorkflowSHA256 || item.Artifact.WorkflowBlobSHA != trace.WorkflowSHA256 {
		return fmt.Errorf("workflow blob SHA for %s/%s does not match trusted manifest SHA", result.Mode, result.Coordinate)
	}

	if item.Run.HeadSHA != event.IntendedSHA || item.Artifact.HeadSHA != event.IntendedSHA {
		return fmt.Errorf("provenance head SHA for %s/%s does not match intended SHA %s", result.Mode, result.Coordinate, event.IntendedSHA)
	}

	if item.Run.BaseSHA != event.BaseSHA || item.Artifact.BaseSHA != event.BaseSHA {
		return fmt.Errorf("provenance base SHA for %s/%s does not match trusted base SHA %s", result.Mode, result.Coordinate, event.BaseSHA)
	}

	if item.Artifact.ProducerRunID != item.Run.ID || item.Artifact.ProducerRunAttempt != item.Run.Attempt {
		return fmt.Errorf("artifact producer run/attempt for %s/%s does not match trusted run metadata", result.Mode, result.Coordinate)
	}

	if item.Artifact.PlanDigest != plan.PlanDigest || item.Artifact.PolicyDigest != plan.PolicyDigest {
		return fmt.Errorf("artifact plan/policy digest for %s/%s does not match evaluated plan", result.Mode, result.Coordinate)
	}

	if item.Artifact.Digest != result.ArtifactDigest {
		return fmt.Errorf("artifact digest for %s/%s does not match accepted result row", result.Mode, result.Coordinate)
	}

	if err := validateDigest("artifact", item.Artifact.Digest); err != nil {
		return err
	}

	if item.Run.StartedAt.IsZero() || item.Run.CompletedAt.IsZero() || item.Run.StartedAt.After(item.Run.CompletedAt) {
		return fmt.Errorf("run timestamps for %s/%s are invalid", result.Mode, result.Coordinate)
	}

	if item.Run.CompletedAt.After(now.Add(artifactUploadSkew)) {
		return fmt.Errorf("run completion time for %s/%s is in the future", result.Mode, result.Coordinate)
	}

	if result.StartedAt.Before(item.Run.StartedAt) || result.CompletedAt.After(item.Run.CompletedAt.Add(artifactUploadSkew)) {
		return fmt.Errorf("result timestamps for %s/%s are not bound to the producer run", result.Mode, result.Coordinate)
	}

	if item.Artifact.UploadedAt.IsZero() ||
		item.Artifact.UploadedAt.Before(item.Run.StartedAt) ||
		item.Artifact.UploadedAt.After(item.Run.CompletedAt.Add(artifactUploadSkew)) {
		return fmt.Errorf("artifact upload time for %s/%s is not bound to the completed producer run", result.Mode, result.Coordinate)
	}

	return nil
}

func workflowTraces(policy cipolicy.Manifest, plan cipolicy.RunPlan) (map[string]cipolicy.LegacyTrace, error) {
	traces := map[string]cipolicy.LegacyTrace{}

	for _, mode := range policy.Modes {
		for _, coordinate := range mode.Coordinates {
			traces[coordinateKey(mode.ID, coordinate.ID)] = coordinate.Trace
		}
	}

	selected := map[string]cipolicy.LegacyTrace{}

	for _, job := range plan.Jobs {
		key := coordinateKey(job.Mode, job.Coordinate)

		trace, ok := traces[key]
		if !ok {
			return nil, fmt.Errorf("manifest trace missing for %s/%s", job.Mode, job.Coordinate)
		}

		selected[key] = trace
	}

	return selected, nil
}

func retainedEvidence(input EvaluationInput, anchors TrustAnchors) (RetainedEvidence, error) {
	retained := RetainedEvidence{
		SchemaVersion:     SchemaVersion,
		EventDeliveryID:   input.Delivery.ID,
		WebhookBodyDigest: input.Delivery.BodyDigest,
		IntendedSHA:       input.Event.IntendedSHA,
		BaseSHA:           input.Event.BaseSHA,
		PolicyDigest:      anchors.Policy.PolicyDigest,
		PlanDigest:        input.Plan.PlanDigest,
		EvaluatorDigest:   anchors.Evaluator.SourceDigest,
		ReleaseDigest:     anchors.Evaluator.ReleaseDigest,
	}

	for _, item := range input.Evidence {
		retained.ProducerRunAttempts = append(retained.ProducerRunAttempts, fmt.Sprintf("%d/%d", item.Run.ID, item.Run.Attempt))
		retained.WorkflowIdentities = append(retained.WorkflowIdentities, item.Run.WorkflowPath+"@"+item.Run.WorkflowBlobSHA)
		retained.ArtifactDigests = append(retained.ArtifactDigests, item.Artifact.Digest)
	}

	sort.Strings(retained.ProducerRunAttempts)
	sort.Strings(retained.WorkflowIdentities)
	sort.Strings(retained.ArtifactDigests)

	digest, err := bundleDigest(input, anchors)
	if err != nil {
		return RetainedEvidence{}, err
	}

	retained.BundleDigest = digest

	return retained, nil
}

func bundleDigest(input EvaluationInput, anchors TrustAnchors) (string, error) {
	type digestPlan struct {
		PlanDigest   string `json:"plan_digest"`
		PolicyDigest string `json:"policy_digest"`
	}

	type digestResult struct {
		Mode           string `json:"mode"`
		Coordinate     string `json:"coordinate"`
		ResultDigest   string `json:"result_digest"`
		ArtifactDigest string `json:"artifact_digest"`
	}

	type digestEvidence struct {
		Mode                       string    `json:"mode"`
		Coordinate                 string    `json:"coordinate"`
		RunID                      int64     `json:"run_id"`
		RunAttempt                 int       `json:"run_attempt"`
		RunRepository              string    `json:"run_repository"`
		RunEvent                   string    `json:"run_event"`
		WorkflowPath               string    `json:"workflow_path"`
		WorkflowBlobSHA            string    `json:"workflow_blob_sha"`
		HeadSHA                    string    `json:"head_sha"`
		BaseSHA                    string    `json:"base_sha"`
		RunStartedAt               time.Time `json:"run_started_at"`
		RunCompletedAt             time.Time `json:"run_completed_at"`
		ArtifactDigest             string    `json:"artifact_digest"`
		ArtifactRepository         string    `json:"artifact_repository"`
		ArtifactProducerRunID      int64     `json:"artifact_producer_run_id"`
		ArtifactProducerRunAttempt int       `json:"artifact_producer_run_attempt"`
		ArtifactPlanDigest         string    `json:"artifact_plan_digest"`
		ArtifactPolicyDigest       string    `json:"artifact_policy_digest"`
		ArtifactUploadedAt         time.Time `json:"artifact_uploaded_at"`
	}

	type digestEvent struct {
		Source              string `json:"source"`
		GitHubEvent         string `json:"github_event"`
		PolicyGitHubEvent   string `json:"policy_github_event"`
		Repository          string `json:"repository"`
		BaseRepository      string `json:"base_repository"`
		HeadRepository      string `json:"head_repository"`
		Ref                 string `json:"ref"`
		BaseRef             string `json:"base_ref"`
		HeadRef             string `json:"head_ref"`
		IntendedSHA         string `json:"intended_sha"`
		HeadSHA             string `json:"head_sha"`
		BaseSHA             string `json:"base_sha"`
		PolicyDigest        string `json:"policy_digest"`
		TrustTier           string `json:"trust_tier"`
		PullRequestFork     bool   `json:"pull_request_fork"`
		SameRepositoryAgent bool   `json:"same_repository_agent"`
		TrustedBase         bool   `json:"trusted_base"`
	}

	type digestInput struct {
		Event     digestEvent       `json:"event"`
		Plan      digestPlan        `json:"plan"`
		Results   []digestResult    `json:"results"`
		Evidence  []digestEvidence  `json:"evidence"`
		Evaluator EvaluatorIdentity `json:"evaluator"`
	}

	results := make([]digestResult, 0, len(input.Results))
	for _, result := range input.Results {
		results = append(results, digestResult{
			Mode:           result.Mode,
			Coordinate:     result.Coordinate,
			ResultDigest:   result.ResultDigest,
			ArtifactDigest: result.ArtifactDigest,
		})
	}

	sort.Slice(results, func(left, right int) bool {
		return results[left].Mode+"\x00"+results[left].Coordinate < results[right].Mode+"\x00"+results[right].Coordinate
	})

	evidence := make([]digestEvidence, 0, len(input.Evidence))
	for _, item := range input.Evidence {
		evidence = append(evidence, digestEvidence{
			Mode:                       item.Mode,
			Coordinate:                 item.Coordinate,
			RunID:                      item.Run.ID,
			RunAttempt:                 item.Run.Attempt,
			RunRepository:              item.Run.Repository,
			RunEvent:                   item.Run.Event,
			WorkflowPath:               item.Run.WorkflowPath,
			WorkflowBlobSHA:            item.Run.WorkflowBlobSHA,
			HeadSHA:                    item.Run.HeadSHA,
			BaseSHA:                    item.Run.BaseSHA,
			RunStartedAt:               item.Run.StartedAt,
			RunCompletedAt:             item.Run.CompletedAt,
			ArtifactDigest:             item.Artifact.Digest,
			ArtifactRepository:         item.Artifact.Repository,
			ArtifactProducerRunID:      item.Artifact.ProducerRunID,
			ArtifactProducerRunAttempt: item.Artifact.ProducerRunAttempt,
			ArtifactPlanDigest:         item.Artifact.PlanDigest,
			ArtifactPolicyDigest:       item.Artifact.PolicyDigest,
			ArtifactUploadedAt:         item.Artifact.UploadedAt,
		})
	}

	sort.Slice(evidence, func(left, right int) bool {
		return evidence[left].Mode+"\x00"+evidence[left].Coordinate < evidence[right].Mode+"\x00"+evidence[right].Coordinate
	})

	// Delivery ids and webhook envelope display fields are replay-checked
	// separately. The bundle key is intentionally limited to evidence identity.
	policyEvent := input.Event.PolicyGitHubEvent
	if policyEvent == "" {
		policyEvent = input.Event.GitHubEvent
	}

	value := digestInput{
		Event: digestEvent{
			Source:              input.Event.Source,
			GitHubEvent:         input.Event.GitHubEvent,
			PolicyGitHubEvent:   policyEvent,
			Repository:          input.Event.Repository,
			BaseRepository:      input.Event.BaseRepository,
			HeadRepository:      input.Event.HeadRepository,
			Ref:                 input.Event.Ref,
			BaseRef:             input.Event.BaseRef,
			HeadRef:             input.Event.HeadRef,
			IntendedSHA:         input.Event.IntendedSHA,
			HeadSHA:             input.Event.HeadSHA,
			BaseSHA:             input.Event.BaseSHA,
			PolicyDigest:        input.Event.PolicyDigest,
			TrustTier:           input.Event.TrustTier,
			PullRequestFork:     input.Event.PullRequestFork,
			SameRepositoryAgent: input.Event.SameRepositoryAgent,
			TrustedBase:         input.Event.TrustedBase,
		},
		Plan: digestPlan{
			PlanDigest:   input.Plan.PlanDigest,
			PolicyDigest: anchors.Policy.PolicyDigest,
		},
		Results:   results,
		Evidence:  evidence,
		Evaluator: anchors.Evaluator,
	}

	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("digest evaluation bundle: %w", err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

func failureEvaluation(input EvaluationInput, now time.Time, reason string) Evaluation {
	headSHA := input.Event.IntendedSHA

	return Evaluation{
		SchemaVersion: SchemaVersion,
		Check: CheckRun{
			Name:        CheckName,
			HeadSHA:     headSHA,
			Status:      "completed",
			Conclusion:  "failure",
			Title:       "CI evidence rejected",
			Summary:     reason,
			CompletedAt: now.UTC(),
		},
		Reasons: []string{reason},
	}
}

func coordinateKey(mode, coordinate string) string {
	return mode + "\x00" + coordinate
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}

	return false
}
