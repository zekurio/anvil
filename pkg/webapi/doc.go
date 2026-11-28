// Package webapi serves the web UI and its JSON API over HTTP. It is a second
// front door onto the same daemon state the control socket exposes, so it
// reuses controlapi.Service for jobs and adds flows, libraries, and Arr
// connections, which live in the store.
package webapi
