package cmd

import (
	"fmt"

	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	rootCmd.AddCommand(pauseCmd)
	rootCmd.AddCommand(resumeCmd)
}

var pauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Pause the operator (scale to 0)",
	RunE:  runPause,
}

var resumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume the operator (scale to 1)",
	RunE:  runResume,
}

func runPause(cmd *cobra.Command, args []string) error {
	return scaleOperator(cmd, 0)
}

func runResume(cmd *cobra.Command, args []string) error {
	return scaleOperator(cmd, 1)
}

func scaleOperator(cmd *cobra.Command, replicas int32) error {
	cs, err := k8s.NewClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	_, err = cs.AppsV1().Deployments(types.Namespace).UpdateScale(ctx, "k3sc-operator", &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: "k3sc-operator", Namespace: types.Namespace},
		Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
	}, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("scale operator: %w", err)
	}

	if replicas == 0 {
		fmt.Println("operator paused (0 replicas)")
	} else {
		fmt.Println("operator resumed (1 replica)")
	}
	return nil
}
