SELECT
  {{ dbt_utils.generate_surrogate_key(['status']) }} AS status_id,
  status
FROM {{ ref('staging_transform_instance') }}
GROUP BY 1, 2
