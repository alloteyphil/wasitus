# ADR-0002: Build and test with my own data only

Date: 26 Nov 2026 · Status: accepted

## Context
The idea came from real incident work at my employer. Using the employer's
telemetry, credentials, systems or work time for a personal project would raise
confidentiality, data-protection (the telemetry can contain customer personal
data) and intellectual-property problems.

## Decision
- All development and testing uses the local demo lab: a fake company with fake
  data, built and owned by me.
- No employer hostnames, service names, error formats, incidents or screenshots in
  this repo, its issues, or its write-ups.
- Work happens on personal time, personal machine accounts and the personal GitHub
  account.
- Any real-world use by an employer happens only after explicit approval, and its
  configuration and private rules stay in the employer's own repositories.

## Consequences
- The scenario suite (staged incidents with known causes) becomes the main evidence
  that the tool works. This is a strength: real incidents rarely have a certain
  ground truth.
- Claims of real-world impact must wait until someone actually runs it.
