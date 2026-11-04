# Spec Delta

## ADDED Requirements

### Requirement: TLS expiry counts as a device answer

A `tls:` check that fails certificate verification, or that fails because the
leaf expires within 7 days or is not yet valid, SHALL be a TLS error for layer
decision. The classifier SHALL treat that error the same way it treats a TCP
refusal or an HTTP status or body mismatch: the device answered, the layer is
`service` once the tunnel is known to work, and the classifier SHALL NOT send
an ICMP echo to decide the layer.

#### Scenario: Certificate expires soon behind a working tunnel

- **WHEN** `nyc-nas`'s `tls:443` check fails with an error containing
  `certificate expires` and a disco ping to `nyc-router` succeeds
- **THEN** the layer is `service` and no ICMP probe is sent

#### Scenario: Certificate name mismatch behind a working tunnel

- **WHEN** `nyc-nas`'s `tls:443` check fails certificate name verification and
  a disco ping to `nyc-router` succeeds
- **THEN** the layer is `service` and no ICMP probe is sent
