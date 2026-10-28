
drop view if exists transform_instance_hmd_lang_transform;
create view transform_instance_hmd_lang_transform as
    select
        id,
        content -> 'instance_name' as instance_name,
        content -> 'status' as status,
        content -> 'run_context' as run_context,
        content -> 'created_at' as created_at,
        content -> 'scheduled_at' as scheduled_at,
        content -> 'started_at' as started_at,
        content -> 'error_message' as error_message,
        content -> 'scheduling_attempts' as scheduling_attempts,
        content -> 'last_scheduling_attempt_at' as last_scheduling_attempt_at,
        content -> 'scheduling_error_message' as scheduling_error_message,
        content -> 'completed_at' as completed_at,
        content -> 'priority' as priority,
        content -> 'pool' as pool,
        content -> 'instance_key' as instance_key,
        content -> 'transform_name' as transform_name,
        content -> 'transform_version' as transform_version,
        created_at, updated_at
    from entity
    where is_deleted = false and name = 'hmd_lang_transform.transform_instance';

