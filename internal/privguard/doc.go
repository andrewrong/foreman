// Package privguard scans text and files for local identity, secrets and
// forbidden file kinds (spec/design/privguard.yaml, rules P1-P4) and redacts
// text before it is stored or shown (ACR-01).
//
// There are no directory or path exemptions: a file name never changes what is
// found in its content. The only exemptions are an inline marker whose reason
// id is registered by the caller (Config.AllowReasons, loaded from
// .privguard/allow.yaml) and, for secrets, a rule plus a specific fingerprint
// (Config.Fingerprints). Owner-specific values (user name, host names) are
// never part of this package; the caller supplies them in Config.
package privguard
