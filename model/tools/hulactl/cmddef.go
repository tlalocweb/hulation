package main

import "fmt"

const help = `hulactl is a command line tool for managing a hulation instance.
Commands:`

type Command struct {
	Name  string
	Help  string
	Usage string
}

const (
	CMD_GENERATEHASH       = "generatehash"
	CMD_GENERATEHASH_HELP  = "Generate a hash from a password"
	CMD_GENERATEHASH_USAGE = "generatehash"
	CMD_AUTH               = "auth"
	CMD_AUTH_HELP          = "Authenticate against a hula server and store credentials"
	CMD_AUTH_USAGE         = "auth [URL]\nURL can be a full URL or just a hostname (https:// is assumed)\nExamples: auth hula.example.com, auth https://hula.example.com:8443"
	CMD_CREATEFORM         = "createform"
	CMD_CREATEFORM_HELP    = "Create a new form"
	CMD_MODIFYFORM         = "modifyform"
	CMD_MODIFYFORM_HELP    = "Modify an existing form"
	CMD_MODIFYFORM_USAGE   = "modifyform [form ID] [PAYLOAD]\nPAYLOAD is a JSON string with the fields to modify\nExample: hulactl modifyform abc123 '{\"name\":\"newname\"}'\nThis will change the name of the form with ID abc123 to 'newname'"
	CMD_SUBMITFORM         = "submitform"
	CMD_SUBMITFORM_HELP    = "Submit a form data (as if on a web form)"
	CMD_DELETEFORM         = "deleteform"
	CMD_DELETEFORM_HELP    = "Delete a form type"
	CMD_LISTFORMS          = "listforms"
	CMD_LISTFORMS_HELP     = "List all form types"
	CMD_CREATELANDER       = "createlander"
	CMD_CREATELANDER_HELP  = "Create a new lander"
	CMD_MODIFYLANDER       = "modifylander"
	CMD_MODIFYLANDER_HELP  = "Modify an existing lander"
	CMD_MODIFYLANDER_USAGE = "modifylander [lander ID] [PAYLOAD]\nPAYLOAD is a JSON string with the fields to modify\nExample: hulactl modifylander abc123 '{\"name\":\"newname\"}'\nThis will change the name of the lander with ID abc123 to 'newname'"
	CMD_DELETELANDER       = "deletelander"
	CMD_DELETELANDER_HELP  = "Delete a lander"
	CMD_LISTLANDERS        = "listlanders"
	CMD_LISTLANDERS_HELP   = "List all landers"
	CMD_AUTHOK             = "authok"
	CMD_AUTHOK_HELP        = "Check if hulactl authentication is working"
	CMD_CREATEUSER         = "createuser"
	CMD_CREATEUSER_HELP    = "Create a new user"
	CMD_DELETEUSER         = "deleteuser"
	CMD_DELETEUSER_HELP    = "Delete a user"
	CMD_LISTUSERS          = "listusers"
	CMD_LISTUSERS_HELP     = "List all users"
	CMD_MODIFYUSER         = "modifyuser"
	CMD_MODIFYUSER_HELP    = "Modify a user"
	CMD_LOGOUT             = "logout"
	CMD_LOGOUT_HELP        = "Logout hulactl from hulation. Removes credentials from hulactl.yaml"
	CMD_DELETEDB           = "deletedb"
	CMD_DELETEDB_HELP      = "Delete the database used by hulation"
	CMD_INITDB             = "initdb"
	CMD_INITDB_HELP        = "Initialize the database used by hulation"
	CMD_BADACTORS          = "badactors"
	CMD_BADACTORS_HELP     = "List bad actors with scores and blocked status"
	CMD_UPDATEADMINHASH       = "updateadminhash"
	CMD_UPDATEADMINHASH_HELP  = "Generate a password hash and write it to the hulation config file"
	CMD_UPDATEADMINHASH_USAGE = "updateadminhash\nRequires -hulaconf flag pointing to hulation config file"
	CMD_RELOAD                = "reload"
	CMD_RELOAD_HELP           = "Send SIGHUP to the running hula process to reload config"
	CMD_TOTPKEY               = "totp-key"
	CMD_TOTPKEY_HELP          = "Generate a TOTP encryption key for the config file"
	CMD_TOTPSETUP             = "totp-setup"
	CMD_TOTPSETUP_HELP        = "Set up TOTP for the admin user (interactive)"
	CMD_SETPASSWORD           = "set-password"
	CMD_SETPASSWORD_HELP      = "Set / rotate a password via OPAQUE PAKE registration. Defaults to admin."
	CMD_SETPASSWORD_USAGE     = "set-password [--username admin] [--provider admin]\nPrompts for the new password (or reads HULACTL_NEW_PASSWORD).\nServer-side stores an OPAQUE registration record; the password\nitself is never sent over the wire."
	CMD_OPAQUESEED            = "opaque-seed"
	CMD_OPAQUESEED_HELP       = "Generate base64url OPAQUE OPRF seed + AKE secret for hula config"
	CMD_FORGETOPAQUE          = "forget-opaque-record"
	CMD_FORGETOPAQUE_HELP     = "EMERGENCY: delete an OPAQUE record from a Bolt file (offline recovery)"
	CMD_FORGETOPAQUE_USAGE    = "hulactl --bolt <path> forget-opaque-record <provider> <username>\nUse only when the live admin password is lost. hula MUST be stopped first\n(Bolt allows only one process to hold the file open). Caller is responsible\nfor copy-out / edit / copy-back; this binary does the edit step.\nNote: flags MUST come BEFORE the command name (Go flag-package convention)."
	CMD_ROTATECOOKIELESS       = "rotate-cookieless-salt"
	CMD_ROTATECOOKIELESS_HELP  = "Replace the cookieless visitor-id salt for a server (Phase 4c.3)"
	CMD_ROTATECOOKIELESS_USAGE = "hulactl --bolt <path> rotate-cookieless-salt <server_id>\nGenerates 32 fresh random bytes and stores them in the cookieless_salts\nbucket for <server_id>. Yesterday's visitors become unrecognisable today —\nthis is the correct behaviour for 'wipe everyone'. hula MUST be stopped\nfirst (Bolt single-writer)."
	CMD_BUILDSITE             = "build"
	CMD_BUILDSITE_HELP        = "Trigger a site build for a server"
	CMD_BUILDSITE_USAGE       = "build <server-id>\nTriggers a site build and polls until complete"
	CMD_BUILDSTATUS           = "build-status"
	CMD_BUILDSTATUS_HELP      = "Get the status of a site build"
	CMD_BUILDSTATUS_USAGE     = "build-status <build-id>"
	CMD_BUILDS                = "builds"
	CMD_BUILDS_HELP           = "List recent builds for a server"
	CMD_BUILDS_USAGE          = "builds <server-id>"
	CMD_STAGING_BUILD         = "staging-build"
	CMD_STAGING_BUILD_HELP    = "Trigger a rebuild in the staging container"
	CMD_STAGING_BUILD_USAGE   = "staging-build <server-id>"
	CMD_STAGING_UPDATE        = "staging-update"
	CMD_STAGING_UPDATE_HELP   = "Upload a file to the staging site via WebDAV"
	CMD_STAGING_UPDATE_USAGE  = "staging-update <server-id> <local-file> <remote-path>"
	CMD_STAGING_GET           = "staging-get"
	CMD_STAGING_GET_HELP      = "Download a file from the staging site via WebDAV"
	CMD_STAGING_GET_USAGE     = "staging-get <server-id> <remote-path> <local-file>\n\nMirror of staging-update — fetches <remote-path> off the staging\nserver and writes it to <local-file>. Atomic via temp+rename."
	CMD_STAGING_MOUNT         = "staging-mount"
	CMD_STAGING_MOUNT_HELP    = "Mount a local folder synced to a staging site via WebDAV"
	CMD_STAGING_MOUNT_USAGE   = "staging-mount <server-id> <folder-mount-point>\nSyncs local folder with remote staging directory. Runs until CTRL-C.\nFlags:\n  --autobuild  trigger a staging build automatically after changes are synced\n  --dangerous  allow syncing executables and security-sensitive files"
	CMD_STAGE                 = "stage"
	CMD_STAGE_HELP            = "Stage edits in a staging server's git working tree (requires hula_build: staging)"
	CMD_STAGE_USAGE           = "stage <server-id> [<path> ...]\n\nWith no <path> arguments, stages every change (`git add -A`).\nWith one or more <path> arguments, stages only those paths.\nPaths must stay inside the staging working tree (no ..).\nThe server refuses if the named server isn't `hula_build: staging`\nor if its staging_src_dir isn't a git working tree."
	CMD_COMMIT                = "commit"
	CMD_COMMIT_HELP           = "Commit staged edits in a staging server's git working tree"
	CMD_COMMIT_USAGE          = "commit <server-id> <message>\n\nCommits whatever's currently staged. Hula appends a\n`Committed-by: Hula` trailer to the message on its own line.\nFlags:\n  --author-name   override the committer name (default: hula-staging)\n  --author-email  override the committer email"
	CMD_PUSH                  = "push"
	CMD_PUSH_HELP             = "Push a staging server's HEAD to origin on the configured branch"
	CMD_PUSH_USAGE            = "push <server-id>\n\nPushes HEAD of staging_src_dir to origin/<branch>, where <branch>\nis whatever's set under root_git_autodeploy.ref.branch in the\nhula config. Auth credentials come from the same root_git_autodeploy.creds\nblock CloneOrPull uses, so make sure those env vars are still in scope."
	CMD_PULL                  = "pull"
	CMD_PULL_HELP             = "Pull origin/<branch> updates into a staging server's working tree (rebase on top, rewind on conflict)"
	CMD_PULL_USAGE            = "pull <server-id>\n\nFetches origin/<branch> and rebases the staging working tree on\ntop. Refuses if the working tree has uncommitted edits (commit\nthem first). On a rebase conflict, hula automatically rewinds\nthe tree to the pre-pull HEAD so the served site keeps working;\nyou'll see a 'rewound to <SHA>' notice in the output."
	CMD_SYNC                  = "sync"
	CMD_SYNC_HELP             = "Pull then push in a single API call (with conflict rewind on either side)"
	CMD_SYNC_USAGE            = "sync <server-id>\n\nEquivalent to `hulactl pull <id> && hulactl push <id>`, but in\none server-side operation. If the pull rebase conflicts, hula\nrewinds and reports the conflict — push is not attempted. If\nthe pull succeeds but the push is rejected, hula rewinds the\nworking tree to the pre-sync HEAD so the served site reverts\nto its known-good state, then reports the push failure."
	CMD_CREATE_AGENT          = "create-agent"
	CMD_CREATE_AGENT_HELP     = "Generate an mTLS-secured agent config yaml for hulaagent (Phase 1: offline)"
	CMD_CREATE_AGENT_USAGE    = "create-agent [-c template.yaml] [--allow-<verb>=<site>[,opts]]... [--expires-in=DUR] [--hula-host=HOST] > agent.yaml\n\nProduces an agent yaml on stdout. Two ways to declare permissions:\n  1. Flag form: --allow-<verb>=<site>[,<opts>] (repeatable).\n  2. Template form: -c <yaml> with config.expires-in + sites.<id>.allow.\nThe two compose: flags override template entries.\n\nVerbs: build, staging-build, pull, push, sync, commit, push-file, get-file.\n--expires-in accepts Go durations (8760h), days (30d), or years (1yr).\n\nNote: Phase 1 is OFFLINE — each invocation generates a one-off Agent\nCA. Phase 2 will register the agent with a running hula server."
)

var commands []Command
var commandsMap map[string]Command

func init() {
	commands = append(commands,
		Command{CMD_GENERATEHASH, CMD_GENERATEHASH_HELP, CMD_GENERATEHASH_USAGE},
		Command{CMD_AUTH, CMD_AUTH_HELP, CMD_AUTH_USAGE},
		Command{CMD_CREATEFORM, CMD_CREATEFORM_HELP, ""},
		Command{CMD_SUBMITFORM, CMD_SUBMITFORM_HELP, ""},
		Command{CMD_MODIFYFORM, CMD_MODIFYFORM_HELP, ""},
		Command{CMD_DELETEFORM, CMD_DELETEFORM_HELP, ""},
		Command{CMD_LISTFORMS, CMD_LISTFORMS_HELP, ""},
		Command{CMD_AUTHOK, CMD_AUTHOK_HELP, ""},
		Command{CMD_CREATEUSER, CMD_CREATEUSER_HELP, ""},
		Command{CMD_DELETEUSER, CMD_DELETEUSER_HELP, ""},
		Command{CMD_LISTUSERS, CMD_LISTUSERS_HELP, ""},
		Command{CMD_MODIFYUSER, CMD_MODIFYUSER_HELP, ""},
		Command{CMD_LOGOUT, CMD_LOGOUT_HELP, ""},
		Command{CMD_DELETEDB, CMD_DELETEDB_HELP, ""},
		Command{CMD_INITDB, CMD_INITDB_HELP, ""},
		Command{CMD_BADACTORS, CMD_BADACTORS_HELP, ""},
		Command{CMD_UPDATEADMINHASH, CMD_UPDATEADMINHASH_HELP, CMD_UPDATEADMINHASH_USAGE},
		Command{CMD_RELOAD, CMD_RELOAD_HELP, ""},
		Command{CMD_TOTPKEY, CMD_TOTPKEY_HELP, ""},
		Command{CMD_TOTPSETUP, CMD_TOTPSETUP_HELP, ""},
		Command{CMD_SETPASSWORD, CMD_SETPASSWORD_HELP, CMD_SETPASSWORD_USAGE},
		Command{CMD_OPAQUESEED, CMD_OPAQUESEED_HELP, ""},
		Command{CMD_FORGETOPAQUE, CMD_FORGETOPAQUE_HELP, CMD_FORGETOPAQUE_USAGE},
		Command{CMD_ROTATECOOKIELESS, CMD_ROTATECOOKIELESS_HELP, CMD_ROTATECOOKIELESS_USAGE},
		Command{CMD_BUILDSITE, CMD_BUILDSITE_HELP, CMD_BUILDSITE_USAGE},
		Command{CMD_BUILDSTATUS, CMD_BUILDSTATUS_HELP, CMD_BUILDSTATUS_USAGE},
		Command{CMD_BUILDS, CMD_BUILDS_HELP, CMD_BUILDS_USAGE},
		Command{CMD_STAGING_BUILD, CMD_STAGING_BUILD_HELP, CMD_STAGING_BUILD_USAGE},
		Command{CMD_STAGING_UPDATE, CMD_STAGING_UPDATE_HELP, CMD_STAGING_UPDATE_USAGE},
		Command{CMD_STAGING_GET, CMD_STAGING_GET_HELP, CMD_STAGING_GET_USAGE},
		Command{CMD_STAGING_MOUNT, CMD_STAGING_MOUNT_HELP, CMD_STAGING_MOUNT_USAGE},
		Command{CMD_STAGE, CMD_STAGE_HELP, CMD_STAGE_USAGE},
		Command{CMD_COMMIT, CMD_COMMIT_HELP, CMD_COMMIT_USAGE},
		Command{CMD_PUSH, CMD_PUSH_HELP, CMD_PUSH_USAGE},
		Command{CMD_PULL, CMD_PULL_HELP, CMD_PULL_USAGE},
		Command{CMD_SYNC, CMD_SYNC_HELP, CMD_SYNC_USAGE},
		Command{CMD_CREATE_AGENT, CMD_CREATE_AGENT_HELP, CMD_CREATE_AGENT_USAGE},
	)
	// generate map version:
	// map of Command.Name to Command:
	commandsMap = make(map[string]Command)
	for _, c := range commands {
		commandsMap[c.Name] = c
	}
}

func getCommandUsage(cmd string) string {
	_cmd, ok := commandsMap[cmd]
	if ok {
		return _cmd.Usage
	}
	return ""
}

func printHelp() {
	fmt.Println(help)
	for _, c := range commands {
		fmt.Printf("  %-15s %s\n", c.Name, c.Help)
	}
}
