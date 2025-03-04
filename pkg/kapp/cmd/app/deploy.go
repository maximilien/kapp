package app

import (
	"fmt"
	"sort"

	"github.com/cppforlife/go-cli-ui/ui"
	ctlapp "github.com/k14s/kapp/pkg/kapp/app"
	ctlcap "github.com/k14s/kapp/pkg/kapp/clusterapply"
	cmdcore "github.com/k14s/kapp/pkg/kapp/cmd/core"
	cmdtools "github.com/k14s/kapp/pkg/kapp/cmd/tools"
	ctlconf "github.com/k14s/kapp/pkg/kapp/config"
	ctldiff "github.com/k14s/kapp/pkg/kapp/diff"
	"github.com/k14s/kapp/pkg/kapp/logger"
	ctllogs "github.com/k14s/kapp/pkg/kapp/logs"
	ctlres "github.com/k14s/kapp/pkg/kapp/resources"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

type DeployOptions struct {
	ui          ui.UI
	depsFactory cmdcore.DepsFactory
	logger      logger.Logger

	AppFlags            AppFlags
	FileFlags           cmdtools.FileFlags
	DiffFlags           cmdtools.DiffFlags
	ResourceFilterFlags cmdtools.ResourceFilterFlags
	ApplyFlags          ApplyFlags
	DeployFlags         DeployFlags
	ResourceTypesFlags  ResourceTypesFlags
	LabelFlags          LabelFlags
}

func NewDeployOptions(ui ui.UI, depsFactory cmdcore.DepsFactory, logger logger.Logger) *DeployOptions {
	return &DeployOptions{ui: ui, depsFactory: depsFactory, logger: logger}
}

func NewDeployCmd(o *DeployOptions, flagsFactory cmdcore.FlagsFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "deploy",
		Aliases: []string{"d", "dep"},
		Short:   "Deploy app",
		RunE:    func(_ *cobra.Command, _ []string) error { return o.Run() },
		Annotations: map[string]string{
			cmdcore.AppHelpGroup.Key: cmdcore.AppHelpGroup.Value,
		},
		Example: `
  # Deploy app 'app1' based on config files in config/
  kapp deploy -a app1 -f config/

  # Deploy app 'app1' while showing full text diff
  kapp deploy -a app1 -f config/ --diff-changes

  # Deploy app 'app1' based on remote file
  kapp deploy -a app1 \
    -f https://github.com/...download/v0.6.0/crds.yaml \
    -f https://github.com/...download/v0.6.0/release.yaml`,
	}

	setDeployCmdFlags(cmd)

	o.AppFlags.Set(cmd, flagsFactory)
	o.FileFlags.Set(cmd)
	o.DiffFlags.SetWithPrefix("diff", cmd)
	o.ResourceFilterFlags.Set(cmd)
	o.ApplyFlags.SetWithDefaults("", ApplyFlagsDeployDefaults, cmd)
	o.DeployFlags.Set(cmd)
	o.ResourceTypesFlags.Set(cmd)
	o.LabelFlags.Set(cmd)

	return cmd
}

func (o *DeployOptions) Run() error {
	app, coreClient, identifiedResources, err := AppFactory(o.depsFactory, o.AppFlags, o.ResourceTypesFlags, o.logger)
	if err != nil {
		return err
	}

	appLabels, err := o.LabelFlags.AsMap()
	if err != nil {
		return err
	}

	err = app.CreateOrUpdate(appLabels)
	if err != nil {
		return err
	}

	newResources, err := o.newResources()
	if err != nil {
		return err
	}

	newResources, conf, err := ctlconf.NewConfFromResourcesWithDefaults(newResources)
	if err != nil {
		return err
	}

	resTypes := ctlres.NewResourceTypesImpl(coreClient, ctlres.ResourceTypesImplOpts{})
	prep := ctlapp.NewPreparation(resTypes)

	o.DeployFlags.PrepareResourcesOpts.DefaultNamespace = o.AppFlags.NamespaceFlags.Name

	newResources, err = prep.PrepareResources(newResources, o.DeployFlags.PrepareResourcesOpts)
	if err != nil {
		return err
	}

	labelSelector, err := app.LabelSelector()
	if err != nil {
		return err
	}

	labeledResources := ctlres.NewLabeledResources(labelSelector, identifiedResources, o.logger)

	err = labeledResources.Prepare(newResources, conf.OwnershipLabelMods(), conf.LabelScopingMods(), conf.AdditionalLabels())
	if err != nil {
		return err
	}

	// Grab ns names before they applying filtering
	nsNames := o.nsNames(newResources)

	resourceFilter, err := o.ResourceFilterFlags.ResourceFilter()
	if err != nil {
		return err
	}

	newResources = resourceFilter.Apply(newResources)
	matchingOpts := ctlres.AllAndMatchingOpts{
		SkipResourceOwnershipCheck: o.DeployFlags.OverrideOwnershipOfExistingResources,
		// Prevent accidently overriding kapp state records
		BlacklistedResourcesByLabelKeys: []string{ctlapp.KappIsAppLabelKey},
	}

	existingResources, err := labeledResources.AllAndMatching(newResources, matchingOpts)
	if err != nil {
		return err
	}

	if o.DeployFlags.Patch {
		existingResources, err = ctlres.NewUniqueResources(existingResources).Match(newResources)
		if err != nil {
			return err
		}
	} else {
		if len(newResources) == 0 && !o.DeployFlags.AllowEmpty {
			return fmt.Errorf("Trying to apply empty set of resources which will delete cluster resources. " +
				"Refusing to continue unless --dangerous-allow-empty-list-of-resources is specified.")
		}
	}

	existingResources = resourceFilter.Apply(existingResources)

	changeFactory := ctldiff.NewChangeFactory(conf.RebaseMods(), conf.DiffAgainstLastAppliedFieldExclusionMods())
	changeSetFactory := ctldiff.NewChangeSetFactory(o.DiffFlags.ChangeSetOpts, changeFactory)

	changes, err := ctldiff.NewChangeSetWithTemplates(
		existingResources, newResources, conf.TemplateRules(),
		o.DiffFlags.ChangeSetOpts, changeFactory).Calculate()
	if err != nil {
		return err
	}

	msgsUI := cmdcore.NewDedupingMessagesUI(cmdcore.NewPlainMessagesUI(o.ui))
	clusterChangeFactory := ctlcap.NewClusterChangeFactory(o.ApplyFlags.ClusterChangeOpts, identifiedResources, changeFactory, changeSetFactory, msgsUI)
	clusterChangeSet := ctlcap.NewClusterChangeSet(changes, o.ApplyFlags.ClusterChangeSetOpts, clusterChangeFactory, msgsUI)

	clusterChanges, clusterChangesGraph, err := clusterChangeSet.Calculate()
	if err != nil {
		return err
	}

	changeSetView := ctlcap.NewChangeSetView(ctlcap.ClusterChangesAsChangeViews(clusterChanges), o.DiffFlags.ChangeSetViewOpts)
	changeSetView.Print(o.ui)

	// Validate after showing change set to make it easier to see all resources
	err = prep.ValidateResources(newResources, o.DeployFlags.PrepareResourcesOpts)
	if err != nil {
		return err
	}

	if o.DiffFlags.Run || len(clusterChanges) == 0 {
		return nil
	}

	err = o.ui.AskForConfirmation()
	if err != nil {
		return err
	}

	if o.DeployFlags.Logs {
		cancelLogsCh := make(chan struct{})
		defer func() { close(cancelLogsCh) }()
		go o.showLogs(coreClient, identifiedResources, labelSelector, cancelLogsCh)
	}

	defer func() {
		_, numDeleted, _ := app.GCChanges(o.DeployFlags.AppChangesMaxToKeep, nil)
		if numDeleted > 0 {
			o.ui.PrintLinef("Deleted %d older app changes", numDeleted)
		}
	}()

	touch := ctlapp.Touch{
		App:              app,
		Description:      "update: " + changeSetView.Summary(),
		Namespaces:       nsNames,
		IgnoreSuccessErr: true,
	}

	return touch.Do(func() error {
		return clusterChangeSet.Apply(clusterChangesGraph)
	})
}

func (o *DeployOptions) newResources() ([]ctlres.Resource, error) {
	var allResources []ctlres.Resource

	for _, file := range o.FileFlags.Files {
		fileRs, err := ctlres.NewFileResources(file)
		if err != nil {
			return nil, err
		}

		for _, fileRes := range fileRs {
			resources, err := fileRes.Resources()
			if err != nil {
				return nil, err
			}

			allResources = append(allResources, resources...)
		}
	}

	return allResources, nil
}

func (o *DeployOptions) nsNames(resources []ctlres.Resource) []string {
	uniqNames := map[string]struct{}{}
	names := []string{}
	for _, res := range resources {
		ns := res.Namespace()
		if ns == "" {
			ns = "(cluster)"
		}
		if _, found := uniqNames[ns]; !found {
			names = append(names, ns)
			uniqNames[ns] = struct{}{}
		}
	}
	sort.Strings(names)
	return names
}

const (
	deployLogsAnnKey = "kapp.k14s.io/deploy-logs" // valid value is ''
)

func (o *DeployOptions) showLogs(
	coreClient kubernetes.Interface, identifiedResources ctlres.IdentifiedResources,
	labelSelector labels.Selector, cancelCh chan struct{}) {

	logOpts := ctllogs.PodLogOpts{Follow: true, ContainerTag: true, LinePrefix: "logs"}

	podWatcher := ctlres.FilteringPodWatcher{
		func(pod *corev1.Pod) bool {
			_, found := pod.Annotations[deployLogsAnnKey]
			return o.DeployFlags.LogsAll || found
		},
		identifiedResources.PodResources(labelSelector),
	}

	ctllogs.NewView(logOpts, podWatcher, coreClient, o.ui).Show(cancelCh)
}
