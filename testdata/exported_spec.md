Widget Service — Specification

⸻

1. Purpose

The widget service stores widgets for other teams.

It provides:
	•	A create endpoint
	•	A list endpoint

—

2. Interface

Command

widgetd serve [--port N]

Flag	Meaning
--port	Listen port (default 8080)

3. Data Model

{
  "id": "w-1",
  "name": "example"
}

Widgets must have a unique name.
Deleted widgets are never returned.

Fields:

```go
type Widget struct{ ID, Name string }
```

## Steps

1. Create Store

2. Add Handlers

  ## Embedded Example
  - an indented example

Widgets are cheap.
