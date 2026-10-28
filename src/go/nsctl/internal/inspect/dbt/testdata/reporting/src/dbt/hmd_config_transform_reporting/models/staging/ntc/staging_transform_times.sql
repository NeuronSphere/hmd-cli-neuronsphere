{{ config(
    materialized='incremental',
    unique_key=['identifier'],
    views_enabled=False,
) }}

SELECT
  identifier,
  transform_name,
  transform_version,
  created_at,
  date_diff('second', created_at, scheduled_at) AS time_ready,
  date_diff('second', scheduled_at, started_at) AS time_scheduled,
  date_diff('second', started_at, completed_at) AS time_running,
  date_diff('second', created_at, completed_at) AS time_total,
  status,
  export_date,
  _created,
  _updated,
  p_iso_date as iso_date,
  p_environment as environment
FROM {{ source('ntc_final', 'ntc_instances_export') }}
{% if is_incremental() %}
WHERE _updated >= (
  SELECT coalesce(max(_updated), cast('1900-01-01' as timestamp))
  FROM {{ this }}
)
{% endif %}
