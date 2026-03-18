package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/abix-/k3sc/internal/dispatch"
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/operator"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func init() {
	rootCmd.AddCommand(operatorCmd)
}

var operatorCmd = &cobra.Command{
	Use:   "operator",
	Short: "Run the k3sc operator (long-running controller)",
	RunE:  runOperator,
}

func runOperator(cmd *cobra.Command, args []string) error {
	edt := time.FixedZone("EDT", -4*3600)
	timeEncoder := func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendString(t.In(edt).Format("15:04:05"))
	}
	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.RawZapOpts(
		uberzap.WrapCore(func(c zapcore.Core) zapcore.Core {
			// replace the encoder with one that uses EDT 24h time
			cfg := zapcore.EncoderConfig{
				TimeKey:        "T",
				LevelKey:       "L",
				NameKey:        "N",
				CallerKey:      "",
				MessageKey:     "M",
				StacktraceKey:  "S",
				LineEnding:     zapcore.DefaultLineEnding,
				EncodeLevel:    zapcore.CapitalLevelEncoder,
				EncodeTime:     timeEncoder,
				EncodeDuration: zapcore.StringDurationEncoder,
				EncodeName:     zapcore.FullNameEncoder,
			}
			return zapcore.NewCore(
				zapcore.NewConsoleEncoder(cfg),
				zapcore.Lock(zapcore.AddSync(os.Stderr)),
				zapcore.DebugLevel,
			)
		}),
	)))

	scheme := runtime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	operator.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	cs, err := k8s.NewClient()
	if err != nil {
		return fmt.Errorf("k8s client: %w", err)
	}

	template, err := dispatch.LoadTemplate()
	if err != nil {
		return fmt.Errorf("load template: %w", err)
	}

	reconciler := &operator.Reconciler{
		Client:   mgr.GetClient(),
		K8s:      cs,
		Template: template,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup controller: %w", err)
	}

	ctx := ctrl.SetupSignalHandler()
	go operator.Scanner(ctx, mgr.GetClient(), types.Namespace)

	fmt.Println("[operator] starting")
	return mgr.Start(ctx)
}
