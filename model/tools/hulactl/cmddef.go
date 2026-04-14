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
	CMD_AUTH_HELP          = "Authenticate and store credentials in hulactl.yaml"
	CMD_AUTH_USAGE         = "auth"
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
