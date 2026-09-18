// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package instancecfg loads the per-instance settings of a fusion-wizard deployment: the values
// that differ between instances (runner image, resources, tag name, polling, upstream URLs) and
// therefore must never be written into a WizardDefinition. Helm renders them into a ConfigMap;
// definitions reach them as ${config.<key>} or, for structured values, through Config fields.
package instancecfg

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// ConfigMap keys. Anything else in the ConfigMap is rejected so a typo cannot silently fall back
// to a default.
const (
	KeyRunnerImage      = "runnerImage"
	KeyDefaultResources = "defaultResources"
	KeyTagName          = "tagName"
	KeyArtifactPrefix   = "artifactPrefix"
	KeyChainStepName    = "chainStepName"
	KeyPollInterval     = "pollInterval"
	KeyBuildTimeout     = "buildTimeout"
	KeyForgeURL         = "forgeURL"
	KeyIndexURL         = "indexURL"
	KeyWeaveURL         = "weaveURL"
)

// Defaults for optional keys. They are the values spectra's browser wizards hardcode today.
const (
	DefaultTagName        = "stable"
	DefaultArtifactPrefix = "app."
	DefaultChainStepName  = "run"
	DefaultPollInterval   = 5 * time.Second
	DefaultBuildTimeout   = 10 * time.Minute
)

// Config is the typed, validated instance configuration.
type Config struct {
	RunnerImage      string
	DefaultResources corev1.ResourceRequirements
	TagName          string
	ArtifactPrefix   string
	ChainStepName    string
	PollInterval     time.Duration
	BuildTimeout     time.Duration
	ForgeURL         string
	IndexURL         string
	WeaveURL         string
}

var knownKeys = map[string]struct{}{
	KeyRunnerImage: {}, KeyDefaultResources: {}, KeyTagName: {}, KeyArtifactPrefix: {},
	KeyChainStepName: {}, KeyPollInterval: {}, KeyBuildTimeout: {},
	KeyForgeURL: {}, KeyIndexURL: {}, KeyWeaveURL: {},
}

// Parse validates ConfigMap data and applies defaults. All problems are reported together.
func Parse(data map[string]string) (Config, error) {
	var problems []string

	var unknown []string
	for k := range data {
		if _, ok := knownKeys[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		problems = append(problems, "unknown key(s): "+strings.Join(unknown, ", "))
	}

	str := func(key, def string) string {
		if v := strings.TrimSpace(data[key]); v != "" {
			return v
		}
		return def
	}
	required := func(key string) string {
		v := strings.TrimSpace(data[key])
		if v == "" {
			problems = append(problems, fmt.Sprintf("%s is required", key))
		}
		return v
	}
	duration := func(key string, def time.Duration) time.Duration {
		raw := strings.TrimSpace(data[key])
		if raw == "" {
			return def
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			problems = append(problems, fmt.Sprintf("%s: %q is not a positive duration (e.g. 5s, 10m)", key, raw))
			return def
		}
		return d
	}
	baseURL := func(key string) string {
		raw := required(key)
		if raw == "" {
			return ""
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			problems = append(problems, fmt.Sprintf("%s: %q is not an http(s) URL", key, raw))
			return ""
		}
		return strings.TrimRight(raw, "/")
	}

	cfg := Config{
		RunnerImage:    required(KeyRunnerImage),
		TagName:        str(KeyTagName, DefaultTagName),
		ArtifactPrefix: str(KeyArtifactPrefix, DefaultArtifactPrefix),
		ChainStepName:  str(KeyChainStepName, DefaultChainStepName),
		PollInterval:   duration(KeyPollInterval, DefaultPollInterval),
		BuildTimeout:   duration(KeyBuildTimeout, DefaultBuildTimeout),
		ForgeURL:       baseURL(KeyForgeURL),
		IndexURL:       baseURL(KeyIndexURL),
		WeaveURL:       baseURL(KeyWeaveURL),
	}

	cfg.DefaultResources = defaultResources()
	if raw := strings.TrimSpace(data[KeyDefaultResources]); raw != "" {
		var res corev1.ResourceRequirements
		if err := yaml.UnmarshalStrict([]byte(raw), &res); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", KeyDefaultResources, err))
		} else {
			cfg.DefaultResources = res
		}
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid instance config: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
}

// defaultResources are the limits spectra's browser wizards put on job templates.
func defaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("250m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
	}
}

// Values exposes the scalar settings for ${config.<key>} placeholders. Structured values
// (DefaultResources) are read from the typed Config by the steps that need them.
func (c Config) Values() map[string]string {
	return map[string]string{
		KeyRunnerImage:    c.RunnerImage,
		KeyTagName:        c.TagName,
		KeyArtifactPrefix: c.ArtifactPrefix,
		KeyChainStepName:  c.ChainStepName,
		KeyPollInterval:   c.PollInterval.String(),
		KeyBuildTimeout:   c.BuildTimeout.String(),
		KeyForgeURL:       c.ForgeURL,
		KeyIndexURL:       c.IndexURL,
		KeyWeaveURL:       c.WeaveURL,
	}
}

// Load reads and parses the instance ConfigMap. The reconciler calls it on every reconcile, so
// a changed ConfigMap takes effect without restarting the operator.
func Load(ctx context.Context, r client.Reader, namespace, name string) (Config, error) {
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm); err != nil {
		return Config{}, fmt.Errorf("instance config ConfigMap %s/%s: %w", namespace, name, err)
	}
	return Parse(cm.Data)
}
