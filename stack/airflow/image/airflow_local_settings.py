"""The platform's Airflow settings (Airflow imports this file as every one of
its processes starts, from $AIRFLOW_HOME/config).

The DAG bundles come from a file, when STORSCALE_BUNDLES names one: the
wrapper the scheduler and the DAG processor start in (airflow_bundles.py)
keeps it at the operator's list, which changes as projects with a repo come
and go. Airflow reads the list through its settings, which a process has
only as it started; a task's process, though, is forked from the scheduler
as the task starts and reads the list then, so with the file it finds a
project's bundle the scheduler never knew, and the scheduler needn't start
again (killing the tasks it runs) for a project's sake. Without the file
(the API server, a project's own Airflow) the list is the settings' own.
"""
import json
import os

from airflow.configuration import conf

__all__ = []   # nothing here is a policy


_getjson = conf.getjson


def _bundles_from_file(section, key, fallback=None, **kwargs):
    path = os.environ.get("STORSCALE_BUNDLES")
    if (section, key) == ("dag_processor", "dag_bundle_config_list") and path:
        try:
            with open(path) as f:
                return json.load(f)
        except (OSError, ValueError):
            pass   # not written yet, or half-written: the settings' own list
    return _getjson(section, key, fallback, **kwargs)


conf.getjson = _bundles_from_file
