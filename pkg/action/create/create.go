package create

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/url"
	"strings"

	"github.com/fatih/color"
	"github.com/justwatchcom/gopass/pkg/action"
	"github.com/justwatchcom/gopass/pkg/clipboard"
	"github.com/justwatchcom/gopass/pkg/cui"
	"github.com/justwatchcom/gopass/pkg/fsutil"
	"github.com/justwatchcom/gopass/pkg/out"
	"github.com/justwatchcom/gopass/pkg/pwgen"
	"github.com/justwatchcom/gopass/pkg/store"
	"github.com/justwatchcom/gopass/pkg/store/secret"
	"github.com/justwatchcom/gopass/pkg/store/sub"
	"github.com/justwatchcom/gopass/pkg/termio"
	"github.com/martinhoefling/goxkcdpwgen/xkcdpwgen"
	"github.com/urfave/cli"
)

const (
	defaultLength = 24
)

type storer interface {
	//Get(context.Context, string) (store.Secret, error)
	Set(context.Context, string, store.Secret) error
	Exists(context.Context, string) bool
	//Delete(context.Context, string) error
	MountPoints() []string
}

type creator struct {
	store storer
}

// Create displays the password creation wizard
func Create(ctx context.Context, c *cli.Context, store storer) error {
	s := creator{store: store}
	acts := make(cui.Actions, 0, 5)
	acts = append(acts, cui.Action{Name: "Website Login", Fn: s.createWebsite})
	acts = append(acts, cui.Action{Name: "PIN Code (numerical)", Fn: s.createPIN})
	acts = append(acts, cui.Action{Name: "Generic", Fn: s.createGeneric})
	acts = append(acts, cui.Action{Name: "AWS Secret Key", Fn: s.createAWS})
	acts = append(acts, cui.Action{Name: "GCP Service Account", Fn: s.createGCP})
	act, sel := cui.GetSelection(ctx, "Please select the type of secret you would like to create", "<↑/↓> to change the selection, <→> to select, <ESC> to quit", acts.Selection())
	switch act {
	case "default":
		fallthrough
	case "show":
		return acts.Run(ctx, c, sel)
	default:
		return action.ExitError(ctx, action.ExitAborted, nil, "user aborted")
	}
}

// extractHostname tries to extract the hostname from a URL in a filepath-safe
// way for use in the name of a secret
func extractHostname(in string) string {
	if in == "" {
		return ""
	}
	// help url.Parse by adding a scheme if one is missing. This should still
	// allow for any scheme, but by default we assume http (only for parsing)
	urlStr := in
	if !strings.Contains(urlStr, "://") {
		urlStr = "http://" + urlStr
	}
	u, err := url.Parse(urlStr)
	if ch := fsutil.CleanFilename(u.Hostname()); err == nil && ch != "" {
		return ch
	}
	return fsutil.CleanFilename(in)
}

// createWebsite walks through the website credential creation wizard
func (s *creator) createWebsite(ctx context.Context, c *cli.Context) error {
	var (
		urlStr   string
		username string
		password string
		comment  string
		store    string
		err      error
		genPw    bool
	)
	out.Green(ctx, "Creating Website login ...")
	urlStr, err = termio.AskForString(ctx, "Please enter the URL", "")
	if err != nil {
		return err
	}
	// the hostname is used as part of the name
	hostname := extractHostname(urlStr)
	if hostname == "" {
		return action.ExitError(ctx, action.ExitUnknown, err, "Can not parse URL '%s'. Please use 'gopass edit' to manually create the secret", urlStr)
	}

	username, err = termio.AskForString(ctx, "Please enter the Username/Login", "")
	if err != nil {
		return err
	}

	genPw, err = termio.AskForBool(ctx, "Do you want to generate a new password?", true)
	if err != nil {
		return err
	}

	if genPw {
		password, err = s.createGeneratePassword(ctx)
		if err != nil {
			return err
		}
	} else {
		password, err = termio.AskForPassword(ctx, username)
		if err != nil {
			return err
		}
	}
	comment, _ = termio.AskForString(ctx, "Comments (optional)", "")

	// select store
	store = cui.AskForStore(ctx, s.store)

	// generate name, ask for override if already taken
	if store != "" {
		store += "/"
	}

	name := fmt.Sprintf("%swebsites/%s/%s", store, fsutil.CleanFilename(hostname), fsutil.CleanFilename(username))
	if s.store.Exists(ctx, name) {
		name, err = termio.AskForString(ctx, "Secret already exists, please choose another path", name)
		if err != nil {
			return err
		}
	}

	sec := secret.New(password, "")
	_ = sec.SetValue("url", urlStr)
	_ = sec.SetValue("username", username)
	_ = sec.SetValue("comment", comment)
	if err := s.store.Set(sub.WithReason(ctx, "Created new entry"), name, sec); err != nil {
		return action.ExitError(ctx, action.ExitEncrypt, err, "failed to set '%s': %s", name, err)
	}

	return s.createPrintOrCopy(ctx, c, name, password, genPw)
}

// createPrintOrCopy will display the created password (or copy to clipboard)
func (s *creator) createPrintOrCopy(ctx context.Context, c *cli.Context, name, password string, genPw bool) error {
	if !genPw {
		return nil
	}

	if c.Bool("print") {
		fmt.Fprintf(
			out.Stdout,
			"The generated password for %s is:\n%s\n", name,
			color.YellowString(password),
		)
		return nil
	}

	if err := clipboard.CopyTo(ctx, name, []byte(password)); err != nil {
		return action.ExitError(ctx, action.ExitIO, err, "failed to copy to clipboard: %s", err)
	}
	return nil
}

// createPIN will walk through the numerical password (PIN) wizard
func (s *creator) createPIN(ctx context.Context, c *cli.Context) error {
	var (
		authority   string
		application string
		password    string
		comment     string
		store       string
		err         error
		genPw       bool
	)
	out.Green(ctx, "Creating numerical PIN ...")
	authority, err = termio.AskForString(ctx, "Please enter the authoriy (e.g. MyBank) this PIN is for", "")
	if err != nil {
		return err
	}
	if authority == "" {
		return action.ExitError(ctx, action.ExitUnknown, nil, "Authority must not be empty")
	}
	application, err = termio.AskForString(ctx, "Please enter the entity (e.g. Credit Card) this PIN is for", "")
	if err != nil {
		return err
	}
	if application == "" {
		return action.ExitError(ctx, action.ExitUnknown, nil, "Application must not be empty")
	}
	genPw, err = termio.AskForBool(ctx, "Do you want to generate a new PIN?", true)
	if err != nil {
		return err
	}
	if genPw {
		password, err = s.createGeneratePIN(ctx)
		if err != nil {
			return err
		}
	} else {
		password, err = termio.AskForPassword(ctx, "PIN")
		if err != nil {
			return err
		}
	}
	comment, _ = termio.AskForString(ctx, "Comments (optional)", "")

	// select store
	store = cui.AskForStore(ctx, s.store)

	// generate name, ask for override if already taken
	if store != "" {
		store += "/"
	}
	name := fmt.Sprintf("%spins/%s/%s", store, fsutil.CleanFilename(authority), fsutil.CleanFilename(application))
	if s.store.Exists(ctx, name) {
		name, err = termio.AskForString(ctx, "Secret already exists, please choose another path", name)
		if err != nil {
			return err
		}
	}
	sec := secret.New(password, "")
	_ = sec.SetValue("application", application)
	_ = sec.SetValue("comment", comment)
	if err := s.store.Set(sub.WithReason(ctx, "Created new entry"), name, sec); err != nil {
		return action.ExitError(ctx, action.ExitEncrypt, err, "failed to set '%s': %s", name, err)
	}

	return s.createPrintOrCopy(ctx, c, name, password, genPw)
}

// createAWS will walk through the AWS credential creation wizard
func (s *creator) createAWS(ctx context.Context, c *cli.Context) error {
	var (
		account   string
		username  string
		accesskey string
		secretkey string
		region    string
		store     string
		err       error
	)
	out.Green(ctx, "Creating AWS credentials ...")
	account, err = termio.AskForString(ctx, "Please enter the AWS Account this key belongs to", "")
	if err != nil {
		return err
	}
	if account == "" {
		return action.ExitError(ctx, action.ExitUnknown, nil, "Account must not be empty")
	}
	username, err = termio.AskForString(ctx, "Please enter the name of the AWS IAM User this key belongs to", "")
	if err != nil {
		return err
	}
	if username == "" {
		return action.ExitError(ctx, action.ExitUnknown, nil, "Username must not be empty")
	}
	accesskey, err = termio.AskForString(ctx, "Please enter the Access Key ID (AWS_ACCESS_KEY_ID)", "")
	if err != nil {
		return err
	}
	secretkey, err = termio.AskForPassword(ctx, "Please enter the Secret Access Key (AWS_SECRET_ACCESS_KEY)")
	if err != nil {
		return err
	}
	region, _ = termio.AskForString(ctx, "Please enter the default Region (AWS_DEFAULT_REGION) (optional)", "")

	// select store
	store = cui.AskForStore(ctx, s.store)

	// generate name, ask for override if already taken
	if store != "" {
		store += "/"
	}
	name := fmt.Sprintf("%saws/iam/%s/%s", store, fsutil.CleanFilename(account), fsutil.CleanFilename(username))
	if s.store.Exists(ctx, name) {
		name, err = termio.AskForString(ctx, "Secret already exists, please choose another path", name)
		if err != nil {
			return err
		}
	}
	sec := secret.New(secretkey, "")
	_ = sec.SetValue("account", account)
	_ = sec.SetValue("username", username)
	_ = sec.SetValue("accesskey", accesskey)
	_ = sec.SetValue("region", region)
	if err := s.store.Set(sub.WithReason(ctx, "Created new entry"), name, sec); err != nil {
		return action.ExitError(ctx, action.ExitEncrypt, err, "failed to set '%s': %s", name, err)
	}
	return nil
}

// createGCP will walk through the GCP credential creation wizard
func (s *creator) createGCP(ctx context.Context, c *cli.Context) error {
	var (
		project  string
		username string
		svcaccfn string
		store    string
		err      error
	)
	out.Green(ctx, "Creating GCP credentials ...")
	svcaccfn, err = termio.AskForString(ctx, "Please enter path to the Service Account JSON file", "")
	if err != nil {
		return err
	}
	buf, err := ioutil.ReadFile(svcaccfn)
	if err != nil {
		return err
	}
	username, project, err = extractGCPInfo(buf)
	if err != nil {
		return err
	}
	if username == "" {
		username, err = termio.AskForString(ctx, "Please enter the name of this service account", "")
		if err != nil {
			return err
		}
	}
	if username == "" {
		return action.ExitError(ctx, action.ExitUnknown, nil, "Username must not be empty")
	}
	if project == "" {
		project, err = termio.AskForString(ctx, "Please enter the name of this GCP project", "")
		if err != nil {
			return err
		}
	}
	if project == "" {
		return action.ExitError(ctx, action.ExitUnknown, nil, "Project must not be empty")
	}

	// select store
	store = cui.AskForStore(ctx, s.store)

	// generate name, ask for override if already taken
	if store != "" {
		store += "/"
	}
	name := fmt.Sprintf("%sgcp/iam/%s/%s", store, fsutil.CleanFilename(project), fsutil.CleanFilename(username))
	if s.store.Exists(ctx, name) {
		name, err = termio.AskForString(ctx, "Secret already exists, please choose another path", name)
		if err != nil {
			return err
		}
	}
	sec := secret.New("", string(buf))
	if err := s.store.Set(sub.WithReason(ctx, "Created new entry"), name, sec); err != nil {
		return action.ExitError(ctx, action.ExitEncrypt, err, "failed to set '%s': %s", name, err)
	}
	return nil
}

// extractGCPInfo will extract the GCP details from the given json blob
func extractGCPInfo(buf []byte) (string, string, error) {
	var m map[string]string
	if err := json.Unmarshal(buf, &m); err != nil {
		return "", "", err
	}
	p := strings.Split(m["client_email"], "@")
	if len(p) < 2 {
		return "", "", fmt.Errorf("client_email contains no email")
	}
	username := p[0]
	p = strings.Split(p[1], ".")
	if len(p) < 1 {
		return username, "", fmt.Errorf("hostname contains not enough separators")
	}
	return username, p[0], nil
}

// createGeneric will walk through the generic secret wizard
func (s *creator) createGeneric(ctx context.Context, c *cli.Context) error {
	var (
		shortname string
		password  string
		store     string
		err       error
		genPw     bool
	)
	out.Green(ctx, "Creating generic secret ...")
	shortname, err = termio.AskForString(ctx, "Please enter a name for the secret", "")
	if err != nil {
		return err
	}
	if shortname == "" {
		return action.ExitError(ctx, action.ExitUnknown, nil, "Name must not be empty")
	}
	genPw, err = termio.AskForBool(ctx, "Do you want to generate a new password?", true)
	if err != nil {
		return err
	}
	if genPw {
		password, err = s.createGeneratePassword(ctx)
		if err != nil {
			return err
		}
	} else {
		password, err = termio.AskForPassword(ctx, shortname)
		if err != nil {
			return err
		}
	}

	// select store
	store = cui.AskForStore(ctx, s.store)

	// generate name, ask for override if already taken
	if store != "" {
		store += "/"
	}
	name := fmt.Sprintf("%smisc/%s", store, fsutil.CleanFilename(shortname))
	if s.store.Exists(ctx, name) {
		name, err = termio.AskForString(ctx, "Secret already exists, please choose another path", name)
		if err != nil {
			return err
		}
	}
	sec := secret.New(password, "")
	out.Print(ctx, "Enter zero or more key value pairs for this secret:")
	for {
		key, err := termio.AskForString(ctx, "Name for Key Value pair (enter to quit)", "")
		if err != nil {
			return err
		}
		if key == "" {
			break
		}
		val, err := termio.AskForString(ctx, "Value for Key '"+key+"'", "")
		if err != nil {
			return err
		}
		_ = sec.SetValue(key, val)
	}
	if err := s.store.Set(sub.WithReason(ctx, "Created new entry"), name, sec); err != nil {
		return action.ExitError(ctx, action.ExitEncrypt, err, "failed to set '%s': %s", name, err)
	}

	return s.createPrintOrCopy(ctx, c, name, password, genPw)
}

// createGeneratePasssword will walk through the password generation steps
func (s *creator) createGeneratePassword(ctx context.Context) (string, error) {
	xkcd, err := termio.AskForBool(ctx, "Do you want a rememberable password?", true)
	if err != nil {
		return "", err
	}
	if xkcd {
		length, err := termio.AskForInt(ctx, "How many words should be combined into a passphrase?", 4)
		if err != nil {
			return "", err
		}
		g := xkcdpwgen.NewGenerator()
		g.SetNumWords(length)
		g.SetDelimiter(" ")
		g.SetCapitalize(true)
		return string(g.GeneratePassword()), nil
	}

	length, err := termio.AskForInt(ctx, "How long should the password be?", defaultLength)
	if err != nil {
		return "", err
	}
	symbols, err := termio.AskForBool(ctx, "Do you want to include symbols?", false)
	if err != nil {
		return "", err
	}
	return pwgen.GeneratePassword(length, symbols), nil
}

// createGeneratePIN will walk through the PIN generation steps
func (s *creator) createGeneratePIN(ctx context.Context) (string, error) {
	length, err := termio.AskForInt(ctx, "How long should the PIN be?", 4)
	if err != nil {
		return "", err
	}
	return pwgen.GeneratePasswordCharset(length, "0123456789"), nil
}
