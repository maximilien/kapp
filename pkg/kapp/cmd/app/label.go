package app

import (
	"github.com/cppforlife/go-cli-ui/ui"
	cmdcore "github.com/k14s/kapp/pkg/kapp/cmd/core"
	"github.com/k14s/kapp/pkg/kapp/logger"
	"github.com/spf13/cobra"
)

type LabelOptions struct {
	ui          ui.UI
	depsFactory cmdcore.DepsFactory
	logger      logger.Logger

	AppFlags AppFlags
}

func NewLabelOptions(ui ui.UI, depsFactory cmdcore.DepsFactory, logger logger.Logger) *LabelOptions {
	return &LabelOptions{ui: ui, depsFactory: depsFactory, logger: logger}
}

func NewLabelCmd(o *LabelOptions, flagsFactory cmdcore.FlagsFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Print specified app label",
		RunE:  func(_ *cobra.Command, _ []string) error { return o.Run() },
		Annotations: map[string]string{
			cmdcore.AppSupportHelpGroup.Key: cmdcore.AppSupportHelpGroup.Value,
		},
	}
	o.AppFlags.Set(cmd, flagsFactory)
	return cmd
}

func (o *LabelOptions) Run() error {
	app, _, _, err := AppFactory(o.depsFactory, o.AppFlags, ResourceTypesFlags{}, o.logger)
	if err != nil {
		return err
	}

	labelSelector, err := app.LabelSelector()
	if err != nil {
		return err
	}

	o.ui.PrintBlock([]byte(labelSelector.String()))

	return nil
}
