// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package stepstest

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// PythonJob returns a fresh copy of the reference definition: spectra's "Git Python job" wizard
// expressed in the step catalogue (watcher, build, tag, template, chain, one trigger per
// entrypoint). Tests mutate the copy freely.
func PythonJob() *wizardv1.WizardDefinitionSpec {
	return &wizardv1.WizardDefinitionSpec{
		Description: "Build a Python app from a Git repository and schedule its entrypoints",
		Parameters: []wizardv1.WizardParameter{
			{Name: "jobName", Required: true, Pattern: `^[a-z0-9-]+$`},
			{Name: "repoUrl", Required: true},
			{Name: "repoRef", Default: &apiextensionsv1.JSON{Raw: []byte(`"main"`)}},
			{Name: "projectDir", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Relative path containing metadata.yaml (optional)"},
			{Name: "entrypoints", Type: wizardv1.ParameterObjectList, Required: true,
				Description: "One entry per entrypoint file: key (filename), type (OnDemand or Cron), schedule (cron expression, Cron only, empty otherwise)"},
			{Name: "externalAuthMode", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: serviceAccount or oidc — inject a short-lived token into the job pods (set together with externalAuthName)"},
			{Name: "externalAuthName", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: allowlisted ServiceAccount name or OIDC secret name (see weave's external-auth options)"},
			{Name: "externalAuthOverrideMode", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: serviceAccount or oidc — overrides the chain's token injection for this trigger's runs (set together with externalAuthOverrideName)"},
			{Name: "externalAuthOverrideName", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: allowlisted ServiceAccount or OIDC secret name for the trigger-level override"},
		},
		Steps: []wizardv1.WizardStep{
			{Name: "watcher", Type: wizardv1.StepGitWatcher, Params: map[string]string{
				"name": "${params.jobName}", "repoUrl": "${params.repoUrl}", "repoRef": "${params.repoRef}", "projectDir": "${params.projectDir}"}},
			{Name: "build", Type: wizardv1.StepWaitBuild, Params: map[string]string{"watcher": "${steps.watcher.outputs.name}"}},
			{Name: "tag", Type: wizardv1.StepTag, Params: map[string]string{
				"artifactId": "${steps.build.outputs.artifactId}", "artifactName": "${steps.build.outputs.artifactName}",
				"version": "${steps.build.outputs.version}", "tag": "${config.tagName}"}},
			{Name: "template", Type: wizardv1.StepJobTemplate, Params: map[string]string{
				"name": "${params.jobName}", "artifactName": "${steps.build.outputs.artifactName}", "tag": "${steps.tag.outputs.tag}", "image": "${config.runnerImage}"}},
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.jobName}", "jobTemplate": "${steps.template.outputs.name}",
				"externalAuthMode": "${params.externalAuthMode}", "externalAuthName": "${params.externalAuthName}"}},
			{Name: "trigger", Type: wizardv1.StepTrigger, ForEach: "${params.entrypoints}", Params: map[string]string{
				"name": "${params.jobName|k8sName}-${item|stem|k8sName}", "chain": "${steps.chain.outputs.name}",
				"type": "${item.type}", "schedule": "${item.schedule}", "override.ENTRYPOINT": "${item}",
				"externalAuthOverrideMode": "${params.externalAuthOverrideMode}", "externalAuthOverrideName": "${params.externalAuthOverrideName}"}},
		},
	}
}

// PythonService returns a fresh copy of the reference definition: a long-running Python service
// (e.g. a Streamlit app) built from a Git repository (watcher, build, tag, service template, a
// Deploy-kind chain, one OnDemand trigger fired immediately on creation). Unlike PythonJob, there
// is exactly one running process, not one trigger per entrypoint file - ENTRYPOINT for a service
// comes from the app's own metadata.yaml, not a per-run override.
func PythonService() *wizardv1.WizardDefinitionSpec {
	return &wizardv1.WizardDefinitionSpec{
		Description: "Build a long-running Python service (e.g. a Streamlit app) from a Git repository",
		Parameters: []wizardv1.WizardParameter{
			{Name: "serviceName", Required: true, Pattern: `^[a-z0-9-]+$`},
			{Name: "repoUrl", Required: true},
			{Name: "repoRef", Default: &apiextensionsv1.JSON{Raw: []byte(`"main"`)}},
			{Name: "projectDir", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Relative path containing metadata.yaml (optional)"},
			{Name: "port", Type: wizardv1.ParameterNumber, Default: &apiextensionsv1.JSON{Raw: []byte(`8501`)}, Description: "Port the service listens on (Streamlit's default is 8501)"},
			{Name: "ingressName", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "DNS label to expose the service at <ingressName>.<cluster hostSuffix> (optional)"},
		},
		Steps: []wizardv1.WizardStep{
			{Name: "watcher", Type: wizardv1.StepGitWatcher, Params: map[string]string{
				"name": "${params.serviceName}", "repoUrl": "${params.repoUrl}", "repoRef": "${params.repoRef}", "projectDir": "${params.projectDir}"}},
			{Name: "build", Type: wizardv1.StepWaitBuild, Params: map[string]string{"watcher": "${steps.watcher.outputs.name}"}},
			{Name: "tag", Type: wizardv1.StepTag, Params: map[string]string{
				"artifactId": "${steps.build.outputs.artifactId}", "artifactName": "${steps.build.outputs.artifactName}",
				"version": "${steps.build.outputs.version}", "tag": "${config.tagName}"}},
			{Name: "template", Type: wizardv1.StepServiceTemplate, Params: map[string]string{
				"name": "${params.serviceName}", "artifactName": "${steps.build.outputs.artifactName}", "tag": "${steps.tag.outputs.tag}",
				"image": "${config.runnerImage}", "port": "${params.port}", "ingressName": "${params.ingressName}"}},
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.serviceName}", "serviceTemplate": "${steps.template.outputs.name}"}},
			{Name: "trigger", Type: wizardv1.StepTrigger, Params: map[string]string{
				"name": "${params.serviceName}", "chain": "${steps.chain.outputs.name}", "type": "OnDemand", "fireOnCreate": "true"}},
		},
	}
}

// BatchJob returns a fresh copy of the reference definition: spectra's "Git Batch Job" wizard
// (watcher, build, tag, template, chain, one trigger fired immediately on creation).
func BatchJob() *wizardv1.WizardDefinitionSpec {
	return &wizardv1.WizardDefinitionSpec{
		Description: "Build and start a single batch job from a Git repository, branch and subfolder",
		Parameters: []wizardv1.WizardParameter{
			{Name: "jobName", Required: true, Pattern: `^[a-z0-9-]+$`, Description: "Shared Kubernetes name for the watcher, chain, job blueprint and trigger"},
			{Name: "repoUrl", Required: true, Description: "Public HTTP(S) git URL"},
			{Name: "repoRef", Default: &apiextensionsv1.JSON{Raw: []byte(`"main"`)}, Description: "Branch or tag to watch"},
			{Name: "projectDir", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Relative path containing metadata.yaml (optional)"},
			{Name: "triggerType", Default: &apiextensionsv1.JSON{Raw: []byte(`"OnDemand"`)}, Description: "OnDemand or Cron — the batch always starts once immediately either way"},
			{Name: "schedule", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Cron schedule for future runs; only used when triggerType is Cron"},
			{Name: "externalAuthMode", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: serviceAccount or oidc — inject a short-lived token into the job pods (set together with externalAuthName)"},
			{Name: "externalAuthName", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: allowlisted ServiceAccount name or OIDC secret name (see weave's external-auth options)"},
			{Name: "externalAuthOverrideMode", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: serviceAccount or oidc — overrides the chain's token injection for this trigger's runs (set together with externalAuthOverrideName)"},
			{Name: "externalAuthOverrideName", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: allowlisted ServiceAccount or OIDC secret name for the trigger-level override"},
		},
		Steps: []wizardv1.WizardStep{
			{Name: "watcher", Type: wizardv1.StepGitWatcher, Params: map[string]string{
				"name": "${params.jobName}", "repoUrl": "${params.repoUrl}", "repoRef": "${params.repoRef}", "projectDir": "${params.projectDir}"}},
			{Name: "build", Type: wizardv1.StepWaitBuild, Params: map[string]string{"watcher": "${steps.watcher.outputs.name}"}},
			{Name: "tag", Type: wizardv1.StepTag, Params: map[string]string{
				"artifactId": "${steps.build.outputs.artifactId}", "artifactName": "${steps.build.outputs.artifactName}",
				"version": "${steps.build.outputs.version}", "tag": "${config.tagName}"}},
			{Name: "template", Type: wizardv1.StepJobTemplate, Params: map[string]string{
				"name": "${params.jobName}", "artifactName": "${steps.build.outputs.artifactName}", "tag": "${steps.tag.outputs.tag}", "image": "${config.runnerImage}"}},
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.jobName}", "jobTemplate": "${steps.template.outputs.name}",
				"externalAuthMode": "${params.externalAuthMode}", "externalAuthName": "${params.externalAuthName}"}},
			{Name: "trigger", Type: wizardv1.StepTrigger, Params: map[string]string{
				"name": "${params.jobName}", "chain": "${steps.chain.outputs.name}",
				"type": "${params.triggerType}", "schedule": "${params.schedule}", "fireOnCreate": "true",
				"externalAuthOverrideMode": "${params.externalAuthOverrideMode}", "externalAuthOverrideName": "${params.externalAuthOverrideName}"}},
		},
	}
}

// BatchCronJob returns a fresh copy of the reference definition: spectra's "Git BatchCron Job"
// wizard (watcher, build, tag, template, chain, one BatchCron trigger — no fireOnCreate, since
// BatchCron entries each start on their own internal schedule).
func BatchCronJob() *wizardv1.WizardDefinitionSpec {
	return &wizardv1.WizardDefinitionSpec{
		Description: "Build a job and wire up a BatchCron trigger from a pasted list of many cron-scheduled entries",
		Parameters: []wizardv1.WizardParameter{
			{Name: "jobName", Required: true, Pattern: `^[a-z0-9-]+$`, Description: "Shared Kubernetes name for the watcher, chain, and job blueprint"},
			{Name: "repoUrl", Required: true, Description: "Public HTTP(S) git URL"},
			{Name: "repoRef", Default: &apiextensionsv1.JSON{Raw: []byte(`"main"`)}, Description: "Branch or tag to watch"},
			{Name: "projectDir", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Relative path containing metadata.yaml (optional)"},
			{Name: "jobs", Required: true, Description: "YAML or JSON list of {cron, params} entries — validated by weave at creation time"},
			{Name: "externalAuthMode", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: serviceAccount or oidc — inject a short-lived token into the job pods (set together with externalAuthName)"},
			{Name: "externalAuthName", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: allowlisted ServiceAccount name or OIDC secret name (see weave's external-auth options)"},
		},
		Steps: []wizardv1.WizardStep{
			{Name: "watcher", Type: wizardv1.StepGitWatcher, Params: map[string]string{
				"name": "${params.jobName}", "repoUrl": "${params.repoUrl}", "repoRef": "${params.repoRef}", "projectDir": "${params.projectDir}"}},
			{Name: "build", Type: wizardv1.StepWaitBuild, Params: map[string]string{"watcher": "${steps.watcher.outputs.name}"}},
			{Name: "tag", Type: wizardv1.StepTag, Params: map[string]string{
				"artifactId": "${steps.build.outputs.artifactId}", "artifactName": "${steps.build.outputs.artifactName}",
				"version": "${steps.build.outputs.version}", "tag": "${config.tagName}"}},
			{Name: "template", Type: wizardv1.StepJobTemplate, Params: map[string]string{
				"name": "${params.jobName}", "artifactName": "${steps.build.outputs.artifactName}", "tag": "${steps.tag.outputs.tag}", "image": "${config.runnerImage}"}},
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.jobName}", "jobTemplate": "${steps.template.outputs.name}",
				"externalAuthMode": "${params.externalAuthMode}", "externalAuthName": "${params.externalAuthName}"}},
			{Name: "trigger", Type: wizardv1.StepBatchTrigger, Params: map[string]string{
				"name": "${params.jobName}", "chain": "${steps.chain.outputs.name}", "jobs": "${params.jobs}"}},
		},
	}
}
