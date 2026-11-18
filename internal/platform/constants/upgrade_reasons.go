package constants

// Shared reason strings used across upgrade workflows and controller status mapping.
const (
	ReasonUpgradeStarted             = "UpgradeStarted"
	ReasonUpgradeComplete            = "UpgradeComplete"
	ReasonUpgradeFailed              = "UpgradeFailed"
	ReasonInvalidVersion             = "InvalidVersion"
	ReasonDowngradeBlocked           = "DowngradeBlocked"
	ReasonUpgradeVersionIncompatible = "UpgradeVersionIncompatible"
	ReasonImageVersionMismatch       = "ImageVersionMismatch"
)
