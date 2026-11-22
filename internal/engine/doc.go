// Package engine is the Eino ADK runtime for Xalgorix scans. It adapts the
// scan's LLM client and tool registry to Eino, owns checkpointing and approval
// interrupts, and exposes the obligation/recovery seams consumed by
// internal/agent. It never owns scan lifecycle, scope policy or findings.
package engine
