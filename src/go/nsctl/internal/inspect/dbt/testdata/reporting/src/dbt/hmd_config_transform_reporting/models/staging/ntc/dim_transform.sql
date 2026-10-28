SELECT
  {{ dbt_utils.generate_surrogate_key(['transform_name', 'transform_version']) }} AS transform_id,
  transform_name,
  transform_version
FROM {{ ref('staging_transform_instance') }}
GROUP BY 1, 2, 3
