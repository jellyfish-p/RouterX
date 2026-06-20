# Chat Messages Validation Plan

## Goal

Align OpenAI-compatible Chat local validation with the documented request contract by rejecting missing or malformed `messages` before any upstream call.

## Scope

- Add a failing router test for missing, `null`, empty, or non-array `messages`.
- Implement a narrow JSON validation in the relay request parser.
- Return a stable OpenAI-compatible 400 error code: `invalid_chat_messages`.
- Update API, Relay, Protocol, Testing, Traceability, and Apifox documentation.
- Run the protected pricing diff guard before staging and after commit.

## Checklist

- [x] Confirm the failing test catches the current gap.
- [x] Implement the minimal backend validation.
- [x] Update API documentation and Apifox import schema.
- [x] Run targeted and full verification.
- [x] Stage and commit the completed slice.
