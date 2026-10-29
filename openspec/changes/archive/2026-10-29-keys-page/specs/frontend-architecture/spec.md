## REMOVED Requirements

### Requirement: API key overview SHALL show lifetime usage aggregates

**Reason**: The APIs page and its lifetime overview cards and "cost by key" and "tokens by key" panels are replaced by the Keys page, whose spend columns carry explicit periods (today, 7 days and 30 days) and sort the whole key list on the server.

**Migration**: Sort the Keys page (`GET /api/keys`) by a spend column; the key page keeps a key's lifetime totals labelled "Lifetime".
