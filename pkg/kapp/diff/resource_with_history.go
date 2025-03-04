package diff

import (
	"fmt"
	"os"

	ctlres "github.com/k14s/kapp/pkg/kapp/resources"
)

const (
	appliedResAnnKey         = "kapp.k14s.io/original"
	appliedResDiffAnnKey     = "kapp.k14s.io/original-diff" // useful for debugging
	appliedResDiffMD5AnnKey  = "kapp.k14s.io/original-diff-md5"
	appliedResDiffFullAnnKey = "kapp.k14s.io/original-diff-full" // useful for debugging
)

var (
	resourceWithHistoryDebug = os.Getenv("KAPP_DEBUG_RESOURCE_WITH_HISTORY") == "true"
)

type ResourceWithHistory struct {
	resource                                 ctlres.Resource
	changeFactory                            *ChangeFactory
	diffAgainstLastAppliedFieldExclusionMods []ctlres.FieldRemoveMod
}

func NewResourceWithHistory(resource ctlres.Resource,
	changeFactory *ChangeFactory, diffAgainstLastAppliedFieldExclusionMods []ctlres.FieldRemoveMod) ResourceWithHistory {

	return ResourceWithHistory{resource.DeepCopy(), changeFactory, diffAgainstLastAppliedFieldExclusionMods}
}

func (r ResourceWithHistory) HistorylessResource() (ctlres.Resource, error) {
	return resourceWithoutHistory{r.resource}.Resource()
}

func (r ResourceWithHistory) LastAppliedResource() ctlres.Resource {
	recalculatedLastAppliedChanges, expectedDiffMD5, expectedDiff := r.recalculateLastAppliedChange()

	for _, recalculatedLastAppliedChange := range recalculatedLastAppliedChanges {
		md5Matches := recalculatedLastAppliedChange.OpsDiff().MinimalMD5() == expectedDiffMD5

		if resourceWithHistoryDebug {
			fmt.Printf("%s: md5 matches (%t) prev %s recalc %s\n----> pref diff\n%s\n----> recalc diff\n%s\n",
				r.resource.Description(), md5Matches,
				expectedDiffMD5, recalculatedLastAppliedChange.OpsDiff().MinimalMD5(),
				expectedDiff, recalculatedLastAppliedChange.OpsDiff().MinimalString())
		}

		if md5Matches {
			return recalculatedLastAppliedChange.AppliedResource()
		}
	}

	return nil
}

func (r ResourceWithHistory) RecordLastAppliedResource(appliedRes ctlres.Resource) (ctlres.Resource, error) {
	change, err := r.lastAppliedChange(appliedRes)
	if err != nil {
		return nil, err
	}

	// Use compact representation to take as little space as possible
	// because annotation value max length is 262144 characters
	// (https://github.com/k14s/kapp/issues/48).
	appliedResBytes, err := change.AppliedResource().AsCompactBytes()
	if err != nil {
		return nil, err
	}

	diff := change.OpsDiff()

	if resourceWithHistoryDebug {
		fmt.Printf("%s: recording md5 %s\n---> \n%s\n",
			r.resource.Description(), diff.MinimalMD5(), diff.MinimalString())
	}

	t := ctlres.StringMapAppendMod{
		ResourceMatcher: ctlres.AllResourceMatcher{},
		Path:            ctlres.NewPathFromStrings([]string{"metadata", "annotations"}),
		KVs: map[string]string{
			appliedResAnnKey:         string(appliedResBytes),
			appliedResDiffAnnKey:     diff.MinimalString(),
			appliedResDiffMD5AnnKey:  diff.MinimalMD5(),
			appliedResDiffFullAnnKey: diff.FullString(),
		},
	}

	resultRes := r.resource.DeepCopy()

	err = t.Apply(resultRes)
	if err != nil {
		return nil, err
	}

	return resultRes, nil
}

func (r ResourceWithHistory) lastAppliedChange(appliedRes ctlres.Resource) (Change, error) {
	// Remove fields specified to be excluded (as they may be generated
	// by the server, hence would be racy to be rebased)
	removeMods := r.diffAgainstLastAppliedFieldExclusionMods

	existingRes, err := NewResourceWithRemovedFields(r.resource, removeMods).Resource()
	if err != nil {
		return nil, err
	}

	return r.newExactHistorylessChange(existingRes, appliedRes)
}

func (r ResourceWithHistory) recalculateLastAppliedChange() ([]Change, string, string) {
	lastAppliedResBytes := r.resource.Annotations()[appliedResAnnKey]
	lastAppliedDiff := r.resource.Annotations()[appliedResDiffAnnKey]
	lastAppliedDiffMD5 := r.resource.Annotations()[appliedResDiffMD5AnnKey]

	if len(lastAppliedResBytes) == 0 || len(lastAppliedDiffMD5) == 0 {
		return nil, "", ""
	}

	lastAppliedRes, err := ctlres.NewResourceFromBytes([]byte(lastAppliedResBytes))
	if err != nil {
		return nil, "", ""
	}

	// Continue to calculate historyless change with excluded fields
	// (previous kapp versions did so, and we do not want to fallback
	// to diffing against list resources).
	recalculatedChange1, err := r.newExactHistorylessChange(r.resource, lastAppliedRes)
	if err != nil {
		return nil, "", "" // TODO deal with error?
	}

	recalculatedChange2, err := r.lastAppliedChange(lastAppliedRes)
	if err != nil {
		return nil, "", "" // TODO deal with error?
	}

	return []Change{recalculatedChange1, recalculatedChange2}, lastAppliedDiffMD5, lastAppliedDiff
}

func (r ResourceWithHistory) newExactHistorylessChange(existingRes, newRes ctlres.Resource) (Change, error) {
	// If annotations are not removed line numbers will be mismatched
	existingRes, err := resourceWithoutHistory{existingRes}.Resource()
	if err != nil {
		return nil, err
	}

	newRes, err = resourceWithoutHistory{newRes}.Resource()
	if err != nil {
		return nil, err
	}

	return r.changeFactory.NewExactChange(existingRes, newRes)
}

type resourceWithoutHistory struct {
	res ctlres.Resource
}

func (r resourceWithoutHistory) Resource() (ctlres.Resource, error) {
	res := r.res.DeepCopy()

	for _, t := range r.removeAppliedResAnnKeysMods() {
		err := t.Apply(res)
		if err != nil {
			return nil, err
		}
	}

	return res, nil
}

func (resourceWithoutHistory) removeAppliedResAnnKeysMods() []ctlres.ResourceMod {
	return []ctlres.ResourceMod{
		ctlres.FieldRemoveMod{
			ResourceMatcher: ctlres.AllResourceMatcher{},
			Path:            ctlres.NewPathFromStrings([]string{"metadata", "annotations", appliedResAnnKey}),
		},
		ctlres.FieldRemoveMod{
			ResourceMatcher: ctlres.AllResourceMatcher{},
			Path:            ctlres.NewPathFromStrings([]string{"metadata", "annotations", appliedResDiffAnnKey}),
		},
		ctlres.FieldRemoveMod{
			ResourceMatcher: ctlres.AllResourceMatcher{},
			Path:            ctlres.NewPathFromStrings([]string{"metadata", "annotations", appliedResDiffMD5AnnKey}),
		},
		ctlres.FieldRemoveMod{
			ResourceMatcher: ctlres.AllResourceMatcher{},
			Path:            ctlres.NewPathFromStrings([]string{"metadata", "annotations", appliedResDiffFullAnnKey}),
		},
	}
}
