// Package pluginhost re-exports the GoFastr heavy-JS plugin platform.
//
// The platform was built HERE first (Phase 0/1 — the richtext editor forced its
// shape) and extracted into gofastr core in Phase 2, exactly as the master
// plan prescribed ("build the editor concrete first; extract the platform
// from what it actually needs"). The implementation now lives at
// github.com/DonaldMurillo/gofastr/framework/pluginhost; this package is a
// compatibility alias so the plugins in this repo (and the design docs that
// reference pluginhost.*) keep working unchanged.
//
// New code should import the framework package directly.
package pluginhost

import (
	"github.com/DonaldMurillo/gofastr/framework/pluginhost"
)

// Isolation + sandbox constants (protocol-v1.md §2).
const (
	IsolationSandboxOpaque = pluginhost.IsolationSandboxOpaque
	DefaultSandbox         = pluginhost.DefaultSandbox
	BrokerScriptURL        = pluginhost.BrokerScriptURL
	BrokerRouteMethod      = pluginhost.BrokerRouteMethod
)

// Manifest + registration types.
type (
	Manifest           = pluginhost.Manifest
	ClientModule       = pluginhost.ClientModule
	BrokerRegistration = pluginhost.BrokerRegistration
	AssetSpec          = pluginhost.AssetSpec
	AssetServer        = pluginhost.AssetServer
	Attribute          = pluginhost.Attribute
	Field              = pluginhost.Field
	MountConfig        = pluginhost.MountConfig
)

// Function forwards.
var (
	RegisterBrokerRoute   = pluginhost.RegisterBrokerRoute
	UIHostOption          = pluginhost.UIHostOption
	NewAssetServer        = pluginhost.NewAssetServer
	MountMarker           = pluginhost.MountMarker
	Allow                 = pluginhost.Allow
	Guard                 = pluginhost.Guard
	WriteCapabilityDenied = pluginhost.WriteCapabilityDenied
)
