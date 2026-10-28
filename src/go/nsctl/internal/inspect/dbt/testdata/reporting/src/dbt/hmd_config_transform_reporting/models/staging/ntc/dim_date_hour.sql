WITH date_spine AS (
    {{ dbt_utils.date_spine(
        datepart="hour",
        start_date="cast('2022-01-01' as timestamp)",
        end_date="cast('2099-12-31' as timestamp)"
    ) }}
)

SELECT
    {{ dbt_utils.generate_surrogate_key(["date_hour"]) }} AS hour_id,
    date_hour
FROM date_spine
