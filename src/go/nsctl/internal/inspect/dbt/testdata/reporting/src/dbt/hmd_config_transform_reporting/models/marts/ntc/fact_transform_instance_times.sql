 -- depends_on: {{ ref('dim_environment') }}

{{ config(
    materialized='incremental',
    views_enabled=False,
    unique_key=['transform_id', 'environment_id', 'status_id', 'hour_id']
) }}

WITH base AS (
    SELECT
        date_trunc('hour', created_at) AS hour,
        transform_name,
        transform_version,
        environment,
        status,
        SUM(time_ready) AS time_ready_sum,
        SUM(time_scheduled) AS time_scheduled_sum,
        SUM(time_running) AS time_running_sum,
        SUM(time_total) AS time_total_sum,
        COUNT(*) AS instance_count
    FROM {{ ref('staging_transform_times') }}

    {% if is_incremental() %}
      WHERE _updated >= (
        SELECT coalesce(max(dh.date_hour),cast('1900-01-01' as timestamp))
        FROM {{ this }} f
        JOIN {{ ref('dim_date_hour') }} dh ON f.hour_id = dh.hour_id
        JOIN {{ ref('dim_environment') }} e ON f.environment_id = e.environment_id
        WHERE e.environment = '{{ var("environment") }}'
      )
    {% endif %}

    GROUP BY date_trunc('hour', created_at), environment, transform_name, transform_version, status
),

joined AS (
    SELECT
        {{ dbt_utils.generate_surrogate_key(["b.transform_name", "b.transform_version"]) }} AS transform_id,
        {{ dbt_utils.generate_surrogate_key(["b.environment"]) }} AS environment_id,
        {{ dbt_utils.generate_surrogate_key(["b.status"]) }} AS status_id,
        d.hour_id,
        b.instance_count,
        b.time_ready_sum,
        b.time_scheduled_sum,
        b.time_running_sum,
        b.time_total_sum,
        MAX(b.hour) OVER () AS max_date
    FROM base b
    LEFT JOIN {{ ref('dim_date_hour') }} d
      ON b.hour = d.date_hour
)

SELECT
    transform_id,
    environment_id,
    status_id,
    hour_id,
    time_ready_sum,
    time_scheduled_sum,
    time_running_sum,
    time_total_sum,
    instance_count,
    max_date
FROM joined
