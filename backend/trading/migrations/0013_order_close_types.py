from django.db import migrations, models


class Migration(migrations.Migration):

    dependencies = [
        ('trading', '0012_order_option_fields'),
    ]

    operations = [
        migrations.AlterField(
            model_name='order',
            name='trade_type',
            field=models.CharField(
                choices=[
                    ('STOCK',     'Stock'),
                    ('CFD',       'CFD'),
                    ('CFD_CLOSE', 'CFD Close'),
                    ('OPTION',    'Option'),
                    ('OPT_CLOSE', 'Option Close'),
                    ('OPT_EXER',  'Option Exercise'),
                ],
                default='STOCK',
                max_length=9,
            ),
        ),
        migrations.AddField(
            model_name='order',
            name='position_id',
            field=models.IntegerField(blank=True, null=True),
        ),
    ]
