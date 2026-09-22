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
			{Name: "entrypoints", Type: wizardv1.ParameterStringList, Required: true},
		},
		Steps: []wizardv1.WizardStep{
			{Name: "watcher", Type: wizardv1.StepGitWatcher, Params: map[string]string{"name": "${params.jobName}", "repoUrl": "${params.repoUrl}", "repoRef": "${params.repoRef}"}},
			{Name: "build", Type: wizardv1.StepWaitBuild, Params: map[string]string{"watcher": "${steps.watcher.outputs.name}"}},
			{Name: "tag", Type: wizardv1.StepTag, Params: map[string]string{
				"artifactId": "${steps.build.outputs.artifactId}", "artifactName": "${steps.build.outputs.artifactName}",
				"version": "${steps.build.outputs.version}", "tag": "${config.tagName}"}},
			{Name: "template", Type: wizardv1.StepJobTemplate, Params: map[string]string{
				"name": "${params.jobName}", "artifactName": "${steps.build.outputs.artifactName}", "tag": "${steps.tag.outputs.tag}", "image": "${config.runnerImage}"}},
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.jobName}", "jobTemplate": "${steps.template.outputs.name}"}},
			{Name: "trigger", Type: wizardv1.StepTrigger, ForEach: "${params.entrypoints}", Params: map[string]string{
				"name": "${params.jobName|k8sName}-${item|stem|k8sName}", "chain": "${steps.chain.outputs.name}", "override.ENTRYPOINT": "${item}"}},
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
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.jobName}", "jobTemplate": "${steps.template.outputs.name}"}},
			{Name: "trigger", Type: wizardv1.StepTrigger, Params: map[string]string{
				"name": "${params.jobName}", "chain": "${steps.chain.outputs.name}",
				"type": "${params.triggerType}", "schedule": "${params.schedule}", "fireOnCreate": "true"}},
		},
	}
}
