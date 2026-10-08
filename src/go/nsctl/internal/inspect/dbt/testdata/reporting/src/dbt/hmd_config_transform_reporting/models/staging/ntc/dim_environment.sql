SELECT
  {{ dbt_utils.generate_surrogate_key(['environment']) }} AS environment_id,
  environment
FROM {{ ref('staging_transform_instance') }}
GROUP BY 1, 2
