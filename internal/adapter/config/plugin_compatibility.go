package config

import (
	"fmt"
	"path"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func validatePluginVersionCompatibility(cluster *openbaov1alpha1.OpenBaoCluster) error {
	if len(cluster.Spec.Plugins) == 0 {
		return nil
	}
	versions := []string{cluster.Spec.Version}
	if cluster.Status.CurrentVersion != "" {
		versions = append(versions, cluster.Status.CurrentVersion)
	}
	for _, version := range versions {
		modern, err := platformsemver.AtLeast(version, 2, 7, 0)
		if err != nil {
			return fmt.Errorf("validate plugin compatibility: %w", err)
		}
		for _, plugin := range cluster.Spec.Plugins {
			minor := 5
			if plugin.Type == portopenbao.SealTypeKMSPlugin {
				minor = 6
			}
			supported, err := platformsemver.AtLeast(version, 2, minor, 0)
			if err != nil {
				return fmt.Errorf("validate plugin compatibility: %w", err)
			}
			if !supported {
				return fmt.Errorf("spec.plugins[%s/%s] requires OpenBao >= 2.%d.0; "+
					"complete the upgrade before configuring it (version=%q)", plugin.Type, plugin.Name, minor, version)
			}
			if err := validatePluginDeclaration(plugin, modern); err != nil {
				return fmt.Errorf("spec.plugins[%s/%s] (version=%q): %w", plugin.Type, plugin.Name, version, err)
			}
		}
	}
	return nil
}

func validatePluginDeclaration(plugin openbaov1alpha1.Plugin, modern bool) error {
	if (plugin.Image == "") == (plugin.Command == "") {
		return fmt.Errorf("exactly one of image or command is required")
	}
	if !modern && (plugin.Version == "" || plugin.BinaryName == "" || plugin.SHA256Sum == "") {
		return fmt.Errorf("version, binaryName, and sha256sum are required before OpenBao 2.7.0")
	}
	if plugin.Command != "" {
		command := path.Clean(plugin.Command)
		if path.IsAbs(command) || command == "." || command == ".." || strings.HasPrefix(command, "../") {
			return fmt.Errorf("command must be relative to the plugin directory")
		}
		if plugin.Version == "" && plugin.Type != portopenbao.SealTypeKMSPlugin {
			return fmt.Errorf("version is required for command-based auth, secret, and database plugins")
		}
		return nil
	}
	ref, err := name.ParseReference(plugin.Image)
	if err != nil {
		return fmt.Errorf("invalid image reference: %w", err)
	}
	if !modern {
		if _, err := name.ParseReference(plugin.Image + ":" + plugin.Version); err != nil {
			return fmt.Errorf("image and version must form a valid reference before OpenBao 2.7.0; "+
				"use an untagged repository and set version separately: %w", err)
		}
	}
	if _, pinned := ref.(name.Digest); !pinned && plugin.SHA256Sum == "" {
		return fmt.Errorf("sha256sum is required unless image is pinned by digest")
	}
	tag, err := name.NewTag(strings.SplitN(plugin.Image, "@", 2)[0], name.WithDefaultTag(plugin.Version))
	if err != nil {
		return fmt.Errorf("invalid image tag: %w", err)
	}
	if tag.Identifier() == "" {
		return fmt.Errorf("version is required when image has no tag")
	}
	return nil
}
