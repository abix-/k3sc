package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
)

const usageLimitLookback = 15 * time.Minute

func init() {
	rootCmd.AddCommand(dispatchCmd)
}

var dispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "Find eligible GitHub issues and create k8s Jobs",
	RunE:  runDispatch,
}

func RunDispatch() (string, error) {
	return runDispatchInner()
}

func runDispatch(cmd *cobra.Command, args []string) error {
	log, err := runDispatchInner()
	if err != nil {
		return err
	}
	fmt.Print(log)
	return nil
}

func runDispatchInner() (string, error) {
	ctx := context.Background()
	var log []string

	maxSlots := 5
	if v := os.Getenv("MAX_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxSlots = n
		}
	}
	templatePath := os.Getenv("JOB_TEMPLATE")
	if templatePath == "" {
		// find relative to executable, then cwd, then k8s pod path
		exe, _ := os.Executable()
		candidates := []string{
			filepath.Join(filepath.Dir(exe), "manifests", "job-template.yaml"),
			filepath.Join("manifests", "job-template.yaml"),
			"/etc/dispatcher/job-template.yaml",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				templatePath = c
				break
			}
		}
		if templatePath == "" {
			templatePath = candidates[len(candidates)-1]
		}
	}

	loc, _ := time.LoadLocation("America/New_York")
	now := time.Now().In(loc).Format("3:04 PM MST")
	log = append(log, fmt.Sprintf("[dispatcher] %s starting scan", now))

	cs, err := k8s.NewClient()
	if err != nil {
		return "", fmt.Errorf("k8s client: %w", err)
	}

	nowUTC := time.Now().UTC()
	skipBackoff, restored, restoredTo, resetAt, err := k8s.CheckAndRestoreDispatcherBackoff(ctx, cs, k8s.DispatcherCronJobName, nowUTC)
	if err != nil {
		return "", fmt.Errorf("dispatcher backoff state: %w", err)
	}
	if restored && resetAt != nil {
		log = append(log, fmt.Sprintf("[dispatcher] usage limit window ended at %s -- restored cron schedule to %s", resetAt.Format(time.RFC3339), restoredTo))
	}
	if skipBackoff && resetAt != nil {
		log = append(log, fmt.Sprintf("[dispatcher] usage backoff active until %s -- skipping dispatch", resetAt.Format(time.RFC3339)))
		now = time.Now().In(loc).Format("3:04 PM MST")
		log = append(log, fmt.Sprintf("[dispatcher] %s scan complete -- skipped due to usage backoff", now))
		return strings.Join(log, "\n") + "\n", nil
	}

	usageLimitPod, usageLimitLog, err := k8s.FindRecentUsageLimitPod(ctx, cs, usageLimitLookback)
	if err != nil {
		return "", fmt.Errorf("usage limit detection: %w", err)
	}
	if usageLimitPod != nil {
		resetAt, ok := k8s.ParseUsageLimitResetTime(nowUTC, usageLimitLog)
		if !ok {
			resetAt = nowUTC.Add(time.Hour)
			log = append(log, fmt.Sprintf("[dispatcher] usage limit detected but reset time was unparseable -- using fallback %s", resetAt.Format(time.RFC3339)))
		}
		changed, previous, err := k8s.SetDispatcherBackoff(ctx, cs, k8s.DispatcherCronJobName, resetAt)
		if err != nil {
			return "", fmt.Errorf("set dispatcher backoff: %w", err)
		}
		log = append(log, fmt.Sprintf("[dispatcher] Claude usage limit detected in pod %s for %s#%d", usageLimitPod.Name, usageLimitPod.Repo.Name, usageLimitPod.Issue))
		if changed {
			log = append(log, fmt.Sprintf("[dispatcher] changed cron schedule from %s to %s until %s", previous, k8s.DispatcherHourlySchedule, resetAt.Format(time.RFC3339)))
		} else {
			log = append(log, fmt.Sprintf("[dispatcher] cron schedule already %s; backoff window now ends at %s", k8s.DispatcherHourlySchedule, resetAt.Format(time.RFC3339)))
		}
		now = time.Now().In(loc).Format("3:04 PM MST")
		log = append(log, fmt.Sprintf("[dispatcher] %s scan complete -- skipped due to usage backoff", now))
		return strings.Join(log, "\n") + "\n", nil
	}

	// orphan cleanup: find issues with owner labels but no active pod
	owned, err := github.GetOwnedIssues(ctx)
	if err != nil {
		log = append(log, fmt.Sprintf("[dispatcher] orphan check error: %v", err))
	} else if len(owned) > 0 {
		activeSlots, err := k8s.GetActiveSlots(ctx, cs)
		if err != nil {
			log = append(log, fmt.Sprintf("[dispatcher] orphan slot check error: %v", err))
		} else {
			activeAgents := map[string]bool{}
			for _, s := range activeSlots {
				activeAgents[types.AgentName(s)] = true
			}
			for _, issue := range owned {
				if activeAgents[issue.Owner] {
					continue
				}
				// owner label present but no active pod -- orphan
				returnLabel := "ready"
				hasPR, err := github.HasOpenPR(ctx, issue.Repo, issue.Number)
				if err == nil && hasPR {
					returnLabel = "needs-review"
				}
				log = append(log, fmt.Sprintf("[dispatcher] orphan: %s#%d owned by %s but no active pod, returning to %s", issue.Repo.Name, issue.Number, issue.Owner, returnLabel))
				if err := github.UnclaimIssue(ctx, issue.Repo, issue.Number, issue.Owner, returnLabel); err != nil {
					log = append(log, fmt.Sprintf("  UNCLAIM ERROR: %v", err))
				}
			}
		}
	}

	eligible, err := github.GetEligibleIssues(ctx)
	if err != nil {
		log = append(log, fmt.Sprintf("[dispatcher] github error: %v", err))
		return strings.Join(log, "\n") + "\n", nil
	}
	if len(eligible) == 0 {
		log = append(log, "[dispatcher] no eligible issues found")
		return strings.Join(log, "\n") + "\n", nil
	}

	var nums []string
	for _, i := range eligible {
		nums = append(nums, fmt.Sprintf("%s#%d", i.Repo.Name, i.Number))
	}
	log = append(log, fmt.Sprintf("[dispatcher] eligible issues: %s", strings.Join(nums, " ")))

	activeSlots, err := k8s.GetActiveSlots(ctx, cs)
	if err != nil {
		return "", fmt.Errorf("active slots: %w", err)
	}

	slotStrs := make([]string, len(activeSlots))
	for i, s := range activeSlots {
		slotStrs[i] = strconv.Itoa(s)
	}
	log = append(log, fmt.Sprintf("[dispatcher] active jobs: %d, slots in use: %s", len(activeSlots), strings.Join(slotStrs, " ")))

	template, err := os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("read template %s: %w", templatePath, err)
	}

	created := 0
	for _, issue := range eligible {
		if len(activeSlots) >= maxSlots {
			log = append(log, fmt.Sprintf("[dispatcher] at max capacity (%d), stopping", maxSlots))
			break
		}

		slot := -1
		for i := 1; i <= maxSlots; i++ {
			found := false
			for _, s := range activeSlots {
				if s == i {
					found = true
					break
				}
			}
			if !found {
				slot = i
				break
			}
		}
		if slot == -1 {
			log = append(log, "[dispatcher] no free slots available")
			break
		}

		agentName := types.AgentName(slot)
		log = append(log, fmt.Sprintf("[dispatcher] claiming %s#%d for %s (slot %d)", issue.Repo.Name, issue.Number, agentName, slot))

		if err := github.ClaimIssue(ctx, issue.Repo, issue.Number, agentName); err != nil {
			log = append(log, fmt.Sprintf("  CLAIM ERROR: %v", err))
			continue
		}
		log = append(log, fmt.Sprintf("  claimed on github"))

		name, err := k8s.CreateJobFromTemplate(ctx, cs, string(template), issue.Number, slot, issue.Repo.CloneURL())
		if err != nil {
			log = append(log, fmt.Sprintf("  JOB ERROR: %v", err))
		} else {
			log = append(log, fmt.Sprintf("  job.batch/%s created", name))
		}

		activeSlots = append(activeSlots, slot)
		created++
	}

	now = time.Now().In(loc).Format("3:04 PM MST")
	log = append(log, fmt.Sprintf("[dispatcher] %s scan complete -- created %d jobs", now, created))
	return strings.Join(log, "\n") + "\n", nil
}
