

drop view if exists environment_hmd_lang_nsreporting;
create view environment_hmd_lang_nsreporting as
    select
        id,
        content -> 'type' as type,
        created_at, updated_at
    from entity
    where is_deleted = false and name = 'hmd_lang_nsreporting.environment';