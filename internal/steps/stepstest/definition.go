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

// ImageService returns a fresh copy of the reference definition: a long-running service from a
// caller-supplied container image (image-only service template, a Deploy-kind chain, one run-owned
// Deployment through a WeaveRun with imageOverrides). The template and chain are shared by every
// service with the same baseName; each service adds only its own run.
func ImageService() *wizardv1.WizardDefinitionSpec {
	return &wizardv1.WizardDefinitionSpec{
		Description: "Deploy a long-running service from a container image",
		Parameters: []wizardv1.WizardParameter{
			{Name: "baseName", Required: true, Pattern: `^[a-z0-9-]+$`, Description: "Name of the shared service template and chain; services with the same baseName (and port) share them"},
			{Name: "serviceName", Required: true, Pattern: `^[a-z0-9-]+$`, Description: "Name of this service's run, and so of its Deployment"},
			{Name: "image", Required: true, Description: "Full image reference with an explicit tag (not latest) or a digest, within weave's allowed image prefixes"},
			{Name: "imagePullPolicy", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: Always, IfNotPresent or Never"},
			{Name: "port", Type: wizardv1.ParameterNumber, Default: &apiextensionsv1.JSON{Raw: []byte(`8080`)}, Description: "Port the service listens on"},
			{Name: "ingressName", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "DNS label to expose the service at <ingressName>.<cluster hostSuffix> (optional)"},
		},
		Steps: []wizardv1.WizardStep{
			{Name: "template", Type: wizardv1.StepServiceTemplate, Params: map[string]string{
				"name": "${params.baseName}", "image": "${config.runnerImage}", "port": "${params.port}"}},
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.baseName}", "serviceTemplate": "${steps.template.outputs.name}"}},
			{Name: "run", Type: wizardv1.StepRun, Params: map[string]string{
				"name": "${params.serviceName}", "chain": "${steps.chain.outputs.name}", "image": "${params.image}",
				"imagePullPolicy": "${params.imagePullPolicy}", "ingressName": "${params.ingressName}"}},
		},
	}
}

// ImageJob returns a fresh copy of the reference definition: a one-shot job from a caller-supplied
// container image (image-only job template, a Job-kind chain, one WeaveRun with imageOverrides that
// starts right away). The template and chain are shared by every job with the same baseName; each job
// adds only its own run.
func ImageJob() *wizardv1.WizardDefinitionSpec {
	return &wizardv1.WizardDefinitionSpec{
		Description: "Run a one-shot job from a container image",
		Parameters: []wizardv1.WizardParameter{
			{Name: "baseName", Required: true, Pattern: `^[a-z0-9-]+$`, Description: "Name of the shared job template and chain; jobs with the same baseName share them"},
			{Name: "jobName", Required: true, Pattern: `^[a-z0-9-]+$`, Description: "Name of this job's run"},
			{Name: "image", Required: true, Description: "Full image reference with an explicit tag (not latest) or a digest, within weave's allowed image prefixes"},
			{Name: "imagePullPolicy", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: Always, IfNotPresent or Never"},
		},
		Steps: []wizardv1.WizardStep{
			{Name: "template", Type: wizardv1.StepJobTemplate, Params: map[string]string{"name": "${params.baseName}", "image": "${config.runnerImage}"}},
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.baseName}", "jobTemplate": "${steps.template.outputs.name}"}},
			{Name: "run", Type: wizardv1.StepRun, Params: map[string]string{
				"name": "${params.jobName}", "chain": "${steps.chain.outputs.name}", "stepKind": "Job",
				"image": "${params.image}", "imagePullPolicy": "${params.imagePullPolicy}"}},
		},
	}
}

// ImageCronJob returns a fresh copy of the reference definition: a Cron-scheduled job from a
// caller-supplied container image (image-only job template, a Job-kind chain, one Cron trigger whose
// imageOverrides weave copies into every run it creates). The template and chain are shared by every
// job with the same baseName; each job adds only its own trigger.
func ImageCronJob() *wizardv1.WizardDefinitionSpec {
	return &wizardv1.WizardDefinitionSpec{
		Description: "Run a container image on a cron schedule",
		Parameters: []wizardv1.WizardParameter{
			{Name: "baseName", Required: true, Pattern: `^[a-z0-9-]+$`, Description: "Name of the shared job template and chain; jobs with the same baseName share them"},
			{Name: "jobName", Required: true, Pattern: `^[a-z0-9-]+$`, Description: "Name of this job's trigger"},
			{Name: "image", Required: true, Description: "Full image reference with an explicit tag (not latest) or a digest, within weave's allowed image prefixes"},
			{Name: "imagePullPolicy", Default: &apiextensionsv1.JSON{Raw: []byte(`""`)}, Description: "Optional: Always, IfNotPresent or Never"},
			{Name: "schedule", Required: true, Description: "Cron expression"},
		},
		Steps: []wizardv1.WizardStep{
			{Name: "template", Type: wizardv1.StepJobTemplate, Params: map[string]string{"name": "${params.baseName}", "image": "${config.runnerImage}"}},
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.baseName}", "jobTemplate": "${steps.template.outputs.name}"}},
			{Name: "trigger", Type: wizardv1.StepTrigger, Params: map[string]string{
				"name": "${params.jobName}", "chain": "${steps.chain.outputs.name}", "type": "Cron", "schedule": "${params.schedule}",
				"image": "${params.image}", "imagePullPolicy": "${params.imagePullPolicy}"}},
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
