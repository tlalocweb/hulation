package apiobjects

import (
	loglib "github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/store/common"
)

const (
	APIOBJECTS_VER = "v1"

	// User index names
	USER_BY_IDENTITY_INDEX             = "user_by_identity"
	USER_BY_EMAIL_INDEX                = "user_by_email"
	USER_BY_VALIDATION_TOKEN_INDEX     = "user_by_validation_token"
	USER_BY_PASSWORD_RESET_TOKEN_INDEX = "user_by_password_reset_token"

	// Robot index names
	ROBOT_BY_NAME_INDEX      = "robot_by_name"
	ROBOT_BY_NAMESPACE_INDEX = "robot_by_namespace"

	// RegistryProject index names
	REGPROJ_BY_NAME_INDEX        = "regproj_by_name"
	REGPROJ_BY_FOLDER_NAME_INDEX = "regproj_by_folder_name"
	REGPROJ_BY_TENANT_INDEX      = "regproj_by_tenant"

	// EdgeNode index names
	EDGENODE_BY_NAME_INDEX     = "edgenode_by_name"
	EDGENODE_BY_FLEET_ID_INDEX = "edgenode_by_fleet_id"
	EDGENODE_BY_NODE_ID_INDEX  = "edgenode_by_node_id"

	// Fleet index names
	FLEET_BY_NAME_INDEX   = "fleet_by_name"
	FLEET_BY_TENANT_INDEX = "fleet_by_tenant"

	// BackingProvider index names
	BACKINGPROVIDER_BY_NAME_INDEX = "backingprovider_by_name"
	BACKINGPROVIDER_BY_UUID_INDEX = "backingprovider_by_uuid"

	// Backing (Image Backing) index names
	BACKING_BY_REGISTRY_PATH_INDEX = "backing_by_registry_path"
	BACKING_BY_UUID_INDEX          = "backing_by_uuid"

	// BackingEntry index names
	BACKINGENTRY_BY_NAME_INDEX              = "backingentry_by_name"
	BACKINGENTRY_BY_UUID_INDEX              = "backingentry_by_uuid"
	BACKINGENTRY_BY_BACKING_PROVIDER_INDEX  = "backingentry_by_backing_provider"
	BACKINGENTRY_BY_SOURCE_INDEX            = "backingentry_by_source"

	// BackingSyncJob index names
	BACKINGSYNCJOB_BY_UUID_INDEX          = "backingsyncjob_by_uuid"
	BACKINGSYNCJOB_BY_ENTRY_INDEX         = "backingsyncjob_by_entry"
	BACKINGSYNCJOB_BY_STATUS_INDEX        = "backingsyncjob_by_status"
	BACKINGSYNCJOB_BY_CLAIMED_BY_INDEX    = "backingsyncjob_by_claimed_by"

	// RegistryUser index names
	REGISTRYUSER_BY_INHERITED_USER_INDEX   = "registryuser_by_inherited_user"
	REGISTRYUSER_BY_ASSOCIATED_GROUP_INDEX = "registryuser_by_associated_group"
	REGISTRYUSER_BY_USERNAME_INDEX         = "registryuser_by_username"

	// Rollout index names
	ROLLOUT_BY_UUID_INDEX  = "rollout_by_uuid"
	ROLLOUT_BY_NAME_INDEX  = "rollout_by_name"
	ROLLOUT_BY_OWNER_INDEX = "rollout_by_owner"

	// RolloutStep index names
	ROLLOUTSTEP_BY_UUID_INDEX    = "rolloutstep_by_uuid"
	ROLLOUTSTEP_BY_ROLLOUT_INDEX = "rolloutstep_by_rollout"

	// StepTemplate index names
	STEPTEMPLATE_BY_UUID_INDEX = "steptemplate_by_uuid"
	STEPTEMPLATE_BY_NAME_INDEX = "steptemplate_by_name"
	STEPTEMPLATE_BY_TYPE_INDEX = "steptemplate_by_type"

	// Approver index names
	APPROVER_BY_UUID_INDEX  = "approver_by_uuid"
	APPROVER_BY_EMAIL_INDEX = "approver_by_email"

	// Tenant index names
	TENANT_BY_NAME_INDEX = "tenant_by_name"
	TENANT_BY_UUID_INDEX = "tenant_by_uuid"
)

// Role represents a user role assignment
type Role struct {
	Role   string
	Tenant string
}

// UserConfig holds configuration for user operations
type UserConfig struct {
	Storage            common.Storage
	UsersIndexBaseKey  string
	DebugLevel         int
}

// uconfig is the global user configuration
var uconfig *UserConfig

// SetUserConfig sets the global user configuration
func SetUserConfig(cfg *UserConfig) {
	uconfig = cfg
}

// GetUserConfig returns the global user configuration
func GetUserConfig() *UserConfig {
	return uconfig
}

// RobotConfig holds configuration for robot operations
type RobotConfig struct {
	Storage             common.Storage
	RobotsIndexBaseKey  string
	DebugLevel          int
}

// rconfig is the global robot configuration
var rconfig *RobotConfig

// SetRobotConfig sets the global robot configuration
func SetRobotConfig(cfg *RobotConfig) {
	rconfig = cfg
}

// GetRobotConfig returns the global robot configuration
func GetRobotConfig() *RobotConfig {
	return rconfig
}

// RegistryProjectConfig holds configuration for registry project operations
type RegistryProjectConfig struct {
	Storage                    common.Storage
	RegistryProjectsIndexBaseKey string
	DebugLevel                 int
}

// rpconfig is the global registry project configuration
var rpconfig *RegistryProjectConfig

// SetRegistryProjectConfig sets the global registry project configuration
func SetRegistryProjectConfig(cfg *RegistryProjectConfig) {
	rpconfig = cfg
}

// GetRegistryProjectConfig returns the global registry project configuration
func GetRegistryProjectConfig() *RegistryProjectConfig {
	return rpconfig
}

// EdgeNodeConfig holds configuration for edge node operations
type EdgeNodeConfig struct {
	Storage               common.Storage
	EdgeNodesIndexBaseKey string
	DebugLevel            int
	// InternalCAPath is the path to the internal CA certificate for generating EdgeNode client certs
	InternalCAPath string
	// InternalCAKeyPath is the path to the internal CA private key
	InternalCAKeyPath string
}

// enconfig is the global edge node configuration
var enconfig *EdgeNodeConfig

// SetEdgeNodeConfig sets the global edge node configuration
func SetEdgeNodeConfig(cfg *EdgeNodeConfig) {
	enconfig = cfg
}

// GetEdgeNodeConfig returns the global edge node configuration
func GetEdgeNodeConfig() *EdgeNodeConfig {
	return enconfig
}

// BackingProviderConfig holds configuration for backing provider operations
type BackingProviderConfig struct {
	Storage                  common.Storage
	BackingProvidersBaseKey  string
	DebugLevel               int
}

// bpconfig is the global backing provider configuration
var bpconfig *BackingProviderConfig

// SetBackingProviderConfig sets the global backing provider configuration
func SetBackingProviderConfig(cfg *BackingProviderConfig) {
	bpconfig = cfg
}

// GetBackingProviderConfig returns the global backing provider configuration
func GetBackingProviderConfig() *BackingProviderConfig {
	return bpconfig
}

// ImageBackingConfig holds configuration for image backing operations
type ImageBackingConfig struct {
	Storage         common.Storage
	BackingsBaseKey string
	DebugLevel      int
}

// ibconfig is the global image backing configuration
var ibconfig *ImageBackingConfig

// SetImageBackingConfig sets the global image backing configuration
func SetImageBackingConfig(cfg *ImageBackingConfig) {
	ibconfig = cfg
}

// GetImageBackingConfig returns the global image backing configuration
func GetImageBackingConfig() *ImageBackingConfig {
	return ibconfig
}

// FleetConfig holds configuration for fleet operations
type FleetConfig struct {
	Storage           common.Storage
	FleetsIndexBaseKey string
	DebugLevel        int
}

// fconfig is the global fleet configuration
var fconfig *FleetConfig

// SetFleetConfig sets the global fleet configuration
func SetFleetConfig(cfg *FleetConfig) {
	fconfig = cfg
}

// GetFleetConfig returns the global fleet configuration
func GetFleetConfig() *FleetConfig {
	return fconfig
}

// RegistryUserConfig holds configuration for registry user operations
type RegistryUserConfig struct {
	Storage                 common.Storage
	RegistryUsersIndexBaseKey string
	DebugLevel              int
}

// ruconfig is the global registry user configuration
var ruconfig *RegistryUserConfig

// SetRegistryUserConfig sets the global registry user configuration
func SetRegistryUserConfig(cfg *RegistryUserConfig) {
	ruconfig = cfg
}

// GetRegistryUserConfig returns the global registry user configuration
func GetRegistryUserConfig() *RegistryUserConfig {
	return ruconfig
}

// BackingEntryConfig holds configuration for backing entry operations
type BackingEntryConfig struct {
	Storage                 common.Storage
	BackingEntriesBaseKey   string
	DebugLevel              int
}

// beconfig is the global backing entry configuration
var beconfig *BackingEntryConfig

// SetBackingEntryConfig sets the global backing entry configuration
func SetBackingEntryConfig(cfg *BackingEntryConfig) {
	beconfig = cfg
}

// GetBackingEntryConfig returns the global backing entry configuration
func GetBackingEntryConfig() *BackingEntryConfig {
	return beconfig
}

// BackingSyncJobConfig holds configuration for backing sync job operations
type BackingSyncJobConfig struct {
	Storage                 common.Storage
	BackingSyncJobsBaseKey  string
	DebugLevel              int
}

// bsjconfig is the global backing sync job configuration
var bsjconfig *BackingSyncJobConfig

// SetBackingSyncJobConfig sets the global backing sync job configuration
func SetBackingSyncJobConfig(cfg *BackingSyncJobConfig) {
	bsjconfig = cfg
}

// GetBackingSyncJobConfig returns the global backing sync job configuration
func GetBackingSyncJobConfig() *BackingSyncJobConfig {
	return bsjconfig
}

// RolloutConfig holds configuration for rollout operations
type RolloutConfig struct {
	Storage         common.Storage
	RolloutsBaseKey string
	DebugLevel      int
}

// rolloutconfig is the global rollout configuration
var rolloutconfig *RolloutConfig

// SetRolloutConfig sets the global rollout configuration
func SetRolloutConfig(cfg *RolloutConfig) {
	rolloutconfig = cfg
}

// GetRolloutConfig returns the global rollout configuration
func GetRolloutConfig() *RolloutConfig {
	return rolloutconfig
}

// RolloutStepConfig holds configuration for rollout step operations
type RolloutStepConfig struct {
	Storage             common.Storage
	RolloutStepsBaseKey string
	DebugLevel          int
}

// rolloutStepConfig is the global rollout step configuration
var rolloutStepConfig *RolloutStepConfig

// SetRolloutStepConfig sets the global rollout step configuration
func SetRolloutStepConfig(cfg *RolloutStepConfig) {
	rolloutStepConfig = cfg
}

// GetRolloutStepConfig returns the global rollout step configuration
func GetRolloutStepConfig() *RolloutStepConfig {
	return rolloutStepConfig
}

// StepTemplateConfig holds configuration for step template operations
type StepTemplateConfig struct {
	Storage              common.Storage
	StepTemplatesBaseKey string
	DebugLevel           int
}

// stepTemplateConfig is the global step template configuration
var stepTemplateConfig *StepTemplateConfig

// SetStepTemplateConfig sets the global step template configuration
func SetStepTemplateConfig(cfg *StepTemplateConfig) {
	stepTemplateConfig = cfg
}

// GetStepTemplateConfig returns the global step template configuration
func GetStepTemplateConfig() *StepTemplateConfig {
	return stepTemplateConfig
}

// ApproverConfig holds configuration for approver operations
type ApproverConfig struct {
	Storage          common.Storage
	ApproversBaseKey string
	DebugLevel       int
}

// approverConfig is the global approver configuration
var approverConfig *ApproverConfig

// SetApproverConfig sets the global approver configuration
func SetApproverConfig(cfg *ApproverConfig) {
	approverConfig = cfg
}

// GetApproverConfig returns the global approver configuration
func GetApproverConfig() *ApproverConfig {
	return approverConfig
}

// TenantConfig holds configuration for tenant operations
type TenantConfig struct {
	Storage            common.Storage
	TenantsIndexBaseKey string
	DebugLevel         int
}

// tconfig is the global tenant configuration
var tconfig *TenantConfig

// SetTenantConfig sets the global tenant configuration
func SetTenantConfig(cfg *TenantConfig) {
	tconfig = cfg
}

// GetTenantConfig returns the global tenant configuration
func GetTenantConfig() *TenantConfig {
	return tconfig
}

var log = loglib.GetTaggedLogger("apiobjects", "API Objects")
