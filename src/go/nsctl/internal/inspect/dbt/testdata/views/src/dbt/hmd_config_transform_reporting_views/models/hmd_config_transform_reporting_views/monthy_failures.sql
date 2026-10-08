
/*
    Welcome to your first dbt model!
    Did you know that you can also configure models directly within SQL files?
    This will override configurations stated in dbt_project.yml

    Try changing "table" to "view" below
*/

{{ config(materialized='view') }}

select 
    date_trunc('month', dh.date_hour) as month,
    tf.transform_name,
    tf.transform_version,
    e.environment,
    SUM(t.instance_count) as instance_count
from {{ source('ntc', 'fact_transform_instance_count') }} t
join {{ source('ntc', 'dim_date_hour') }} dh on t.hour_id = dh.hour_id
join {{ source('ntc', 'dim_transform') }} tf on t.transform_id = tf.transform_id
join {{ source('ntc', 'dim_environment') }} e on t.environment_id = e.environment_id
join {{ source('ntc', 'dim_status') }} s on t.status_id = s.status_id
where s.status IN ('complete_failed', 'failed_max_attempts')
group by 
    date_trunc('month', dh.date_hour),
    tf.transform_name,
    tf.transform_version,
    e.environment