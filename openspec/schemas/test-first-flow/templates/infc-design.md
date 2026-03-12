# {Interface Name}

## Interface Definition
<!-- Required -->

- **Capability:**
  <!-- Capability this interface belongs to -->

- **Interface Type:**
  <!-- Interface category: HTTP | CLI | Module | SDK | Other -->

- **Interface Name:**
  <!-- Public interface name -->

- **Summary:**
  <!-- Brief description of purpose and behavior -->

## Route Definition
<!-- Include this section ONLY when Interface Type = HTTP -->

- **Method:**
  <!-- HTTP method: GET | POST | PUT | PATCH | DELETE -->

- **Route:**
  <!-- HTTP route path -->

## Parameters
<!-- List only externally visible input parameters -->

| Name   | Type     | Required | Description   |
| ------ | -------- | -------- | ------------- |
| {name} | `{type}` | Yes/No   | {description} |

## Response
<!-- Describe the successful result struct, empty table if the response body is empty or trivial -->

| Name   | Type     | Required | Description   |
| ------ | -------- | -------- | ------------- |
| {name} | `{type}` | Yes/No   | {description} |

## Error
<!-- Describe meaningful caller-visible failure cases -->

### {Short Error Situation Description #1}
- **HTTP Status:** <!-- HTTP Status Code -->
- **Error Code:** <!-- Business Error Code -->
- **Condition:** <!-- when this error happens -->
- **Description:** <!-- what this error means to the caller -->
- **Error Response:** <!-- Specific Error Response for This Situation  -->
- **Notes:** <!-- special handling or tradeoff if any -->

## Notes
<!-- Describe special handling, compatibility constraints, or tradeoffs -->
