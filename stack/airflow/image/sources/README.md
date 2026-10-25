# dlt's verified sources, copied

The platform's connectors (use case 8.4) are dlt's verified sources, copied
here from https://github.com/dlt-hub/verified-sources at commit
`6fe470a0e54e09b065d0f425f967f00a62724ec6` (Apache-2.0: LICENSE beside this file), unchanged.
The Airflow image puts them in the imports' virtualenv (/opt/dlt/sources),
where the import runner (storscale_import_run.py) takes them.

- `salesforce/`: on simple-salesforce; the runner gives it the import's
  objects and the connection's sign-in.

Gong, HighSpot and SAP S/4HANA aren't verified sources: their resources are the
platform's own, in `storscale_connectors.py` (beside the Salesforce sign-in it
builds): Gong's v2 and HighSpot's v1.0 with an API key, S/4HANA's OData V2 APIs
as a communication user.

To update one, copy its folder from a newer commit and change the commit above.
