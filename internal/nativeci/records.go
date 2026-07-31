package nativeci

type managerEvidence struct {
	Stage    string `json:"stage"`
	Manager  string `json:"manager"`
	Identity string `json:"identity"`
	Active   bool   `json:"active"`
	PID      int    `json:"pid"`
}

type permissionEvidence struct {
	Role                 string `json:"role"`
	Mode                 string `json:"mode,omitempty"`
	OwnerCurrentUser     bool   `json:"owner_current_user"`
	ACLCurrentUser       bool   `json:"acl_current_user,omitempty"`
	ACLSystem            bool   `json:"acl_system,omitempty"`
	UnexpectedAllowCount int    `json:"unexpected_allow_count"`
}

type binarySmokeEvidence struct {
	Name          string `json:"name"`
	SHA256        string `json:"sha256"`
	VersionOutput string `json:"version_output"`
	GoVersion     string `json:"go_version"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	Launched      bool   `json:"launched"`
	ExitCode      int    `json:"exit_code"`
}

type architectureSmokeEvidence struct {
	Schema                string                `json:"schema"`
	ReleaseRunID          string                `json:"release_run_id"`
	ReleaseRunAttempt     string                `json:"release_run_attempt"`
	ReleaseTag            string                `json:"release_tag"`
	ReleaseHeadSHA        string                `json:"release_head_sha"`
	ReleaseManifestSHA256 string                `json:"release_manifest_sha256"`
	ReleaseArchive        string                `json:"release_archive"`
	ReleaseArchiveSHA256  string                `json:"release_archive_sha256"`
	ReleaseArtifactName   string                `json:"release_artifact_name"`
	Binaries              []binarySmokeEvidence `json:"binaries"`
}

type mcpEvidence struct {
	Stage                       string   `json:"stage"`
	Endpoint                    string   `json:"endpoint"`
	ServerVersion               string   `json:"server_version"`
	ProtocolVersion             string   `json:"protocol_version"`
	SessionEstablished          bool     `json:"session_established"`
	ToolNames                   []string `json:"tool_names"`
	ReadOnlyToolCount           int      `json:"read_only_tool_count"`
	WriteToolCount              int      `json:"write_tool_count"`
	RepresentativeTool          string   `json:"representative_tool"`
	RepresentativeReadSucceeded bool     `json:"representative_read_succeeded"`
	AuthorizedRequestCount      int      `json:"authorized_request_count"`
	UnauthorizedRequestCount    int      `json:"unauthorized_request_count"`
	MockMethod                  string   `json:"mock_method"`
	MockPath                    string   `json:"mock_path"`
	AuthHeaderValid             bool     `json:"auth_header_valid"`
}

type postCleanupEvidence struct {
	ProfileAbsent bool `json:"profile_absent"`
	ServiceAbsent bool `json:"service_absent"`
	ProcessAbsent bool `json:"process_absent"`
	Port8100Free  bool `json:"port_8100_free"`
}
