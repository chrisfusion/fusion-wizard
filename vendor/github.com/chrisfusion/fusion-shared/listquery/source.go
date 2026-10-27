// SPDX-License-Identifier: GPL-3.0-or-later

package listquery

// Labels that tell where a resource came from.
const (
	// LabelManagedBy classifies who manages a resource ("manual", "wizard", ...).
	LabelManagedBy = "fusion-platform.io/managed-by"
	// LabelFluxKustomization is set by Flux's kustomize-controller on everything
	// it applies.
	LabelFluxKustomization = "kustomize.toolkit.fluxcd.io/name"
)

// SourceGitOps is the source of resources applied by Flux.
const SourceGitOps = "gitops"

// Sources returns where a resource came from: SourceGitOps when Flux applied it
// (or it is labelled managed-by=gitops), plus its managed-by value. A wizard
// resource committed to a GitOps repo has both, so it matches source=gitops and
// source=wizard.
func Sources(labels map[string]string) []string {
	var out []string
	m := labels[LabelManagedBy]
	if labels[LabelFluxKustomization] != "" || m == SourceGitOps {
		out = append(out, SourceGitOps)
	}
	if m != "" && m != SourceGitOps {
		out = append(out, m)
	}
	return out
}
