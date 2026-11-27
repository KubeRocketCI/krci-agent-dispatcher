# Dispatcher chart owns the actor credentials

The dispatcher writes the egress policy of every actor, and the egress policy names the actor credentials (`gitlab`, `anthropic`). So the dispatcher chart creates them, together with the dispatcher credentials: by hand when `eso.enabled` is false, through ESO when it is true. Substrate gives the mechanism only: the credential provider and the credential grant. The alternative was a platform-side install that creates the actor credentials; it couples two systems through names that one sets and the other must make.

Two credential scopes, one Secret group each, no Secret in both: dispatcher credentials (`<fullname>`: `gitlab-token`, `hook-token`) and actor credentials (`gitlab`: `token`, `basic`; `anthropic`: `api-key`). With ESO, one provider secret per scope (`eso.secretPath.dispatcher`, `eso.secretPath.actor`). One bot user puts the same GitLab token in both scopes: the duplication is in the secret store, not in the chart. The alternative was one provider secret with key prefixes.

The chart marks the actor credentials with the label `app.edp.epam.com/actor-credential: "true"`; the credential grant selects that label. All Secrets are in the release namespace.

## Consequences

- One dispatcher install has every Secret its actors use; the Substrate install changes only for a new atespace or grant.
- The egress gateway cannot read the dispatcher credentials, also in the same namespace.
- A separate agent identity changes only the actor scope value.
- One IAM policy per scope path is possible; the chart has one SecretStore for both.
- `basic` is derived from the actor `gitlab-token` (`oauth2:<token>`): a rotation changes both keys.

## Limitations

1. The actor credential names are fixed in the egress policy code: a second release in the same namespace conflicts with the first.
2. The dispatcher chart does not configure Substrate: the credential grant, the atespace and its access policy are install steps of the Substrate side (chart README, section Substrate requirements).
